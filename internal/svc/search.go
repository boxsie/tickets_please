package svc

// Search RPCs (T11). Each method embeds the query, runs a top-k cosine search
// over the right resident vec index (T10 populated all four), and hydrates
// hits from the in-memory cache or disk.
//
// Result types live in this file rather than internal/domain because they are
// service-local — the MCP layer (T12) wraps them as tool outputs and never
// re-exports them through the pure-domain shapes.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/vecindex"
)

// searchDefaultLimit / searchMaxLimit cap each search method's limit param so
// a runaway client can't ask for an unbounded result set. Mirrors
// vecindex.Index's own defaults; we still apply them at the service edge so
// the cap is documented at the public API surface and so downstream filters
// (e.g. ticket Columns post-filter) can't blow past it.
const (
	searchDefaultLimit = 10
	searchMaxLimit     = 50
)

// ProjectHit is one result from SearchProjects. ProjectSlug duplicates
// Project.Slug at the top level so JSON consumers can read provenance without
// having to descend into the embedded project shape — matches the convention
// the LearningHit shape uses.
type ProjectHit struct {
	Project     *domain.Project
	ProjectSlug string
	Score       float32
}

// TicketHit is one result from SearchTickets.
type TicketHit struct {
	Ticket *domain.Ticket
	Score  float32
}

// CommentHit is one result from SearchComments. TicketTitle is the parent
// ticket's title, denormalized so callers don't have to chase another lookup
// to render a "Re: <title>" line.
type CommentHit struct {
	Comment     *domain.Comment
	Score       float32
	TicketTitle string
}

// LearningHit is one result from SearchLearnings. Carries enough context to
// render a result line ("[<project>/<title>]: <learnings excerpt>") without
// re-fetching the ticket. Learnings is the raw section text from
// `completion.md`. ProjectSlug carries the cross-project provenance the
// resident index was tagged with at hydrate / upsert time.
type LearningHit struct {
	TicketID    string
	ProjectID   string
	ProjectSlug string
	Title       string
	Learnings   string
	Score       float32
	CompletedAt time.Time
}

// SearchProjects runs semantic search over the resident SummaryIdx. The same
// index also holds phase summaries (T10 stored both under the same Kind tag);
// this method filters hits down to entries whose ID matches a known project
// id, dropping phase hits silently.
//
// Read-only — no requireSession.
func (s *Service) SearchProjects(ctx context.Context, query string, limit int) ([]ProjectHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("%w: query required", domain.ErrInvalidArgument)
	}
	limit = clampSearchLimit(limit)

	vec, err := s.Embed.Embed(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// Over-fetch by 2x so we still come back with `limit` results after the
	// phase-id filter drops some hits. Cap at the index's own ceiling.
	rawLimit := limit * 2
	if rawLimit > searchMaxLimit*2 {
		rawLimit = searchMaxLimit * 2
	}
	hits := s.SummaryIdx.Search(vec, vecindex.KindProjectSummary, "", rawLimit)
	if len(hits) == 0 {
		return []ProjectHit{}, nil
	}

	// Build an id → slug index for every mounted project so we can both
	// filter out phase hits (whose ids won't be in the map) and route
	// each hit back to the right Store / cache for hydration.
	idToSlug := make(map[string]string)
	walkErr := s.WalkProjectMounts(func(mountSlug string, mount *ProjectMount) error {
		if mount == nil || mount.Store == nil {
			return nil
		}
		// Each Store is single-project post-flatten; the slug we're given on
		// the registry IS the on-disk project slug. Capture project IDs only;
		// skip the per-project WalkProjects error so one broken mount
		// doesn't sink the whole search.
		_ = mount.Store.WalkProjects(func(_ string, rec *store.ProjectRecord) error {
			idToSlug[rec.ID] = mountSlug
			return nil
		})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk project mounts: %w", walkErr)
	}
	// Stdio fallback: when the registry is empty (e.g. a test that built
	// Service without an eager mount and never registered one), still consult
	// the default Store so single-project tests/CLIs see their project.
	if len(idToSlug) == 0 && s.Store != nil {
		_ = s.Store.WalkProjects(func(slug string, rec *store.ProjectRecord) error {
			idToSlug[rec.ID] = slug
			return nil
		})
	}

	out := make([]ProjectHit, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		slug, ok := idToSlug[h.ID]
		if !ok {
			// Phase hit (or project deleted between embed write and search).
			continue
		}
		p, err := s.GetProject(ctx, slug)
		if err != nil {
			// Project vanished mid-walk; skip silently rather than fail the
			// whole call.
			continue
		}
		out = append(out, ProjectHit{Project: p, ProjectSlug: slug, Score: h.Score})
	}
	return out, nil
}

// SearchTickets runs semantic search over the resident TicketsIdx, scoped to
// one project. v1 requires the project filter — global ticket search would
// thrash the cache lazy-loading every project to hydrate hits. The optional
// Columns filter is applied post-hoc against the hydrated ticket.
//
// Read-only — no requireSession.
func (s *Service) SearchTickets(ctx context.Context, in domain.SearchTicketsInput) ([]TicketHit, error) {
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, fmt.Errorf("%w: query required", domain.ErrInvalidArgument)
	}
	if strings.TrimSpace(in.ProjectIDOrSlug) == "" {
		return nil, fmt.Errorf("%w: project_id_or_slug required in v1 (global ticket search would thrash the project cache)", domain.ErrInvalidArgument)
	}
	limit := clampSearchLimit(in.Limit)

	lp, _, err := s.Cache.Get(ctx, in.ProjectIDOrSlug)
	if err != nil {
		return nil, err
	}
	slug := lp.Project.Slug

	vec, err := s.Embed.Embed(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// Over-fetch when Columns post-filter is active so we still return ~limit.
	rawLimit := limit
	if len(in.Columns) > 0 {
		rawLimit = limit * 4
		if rawLimit > searchMaxLimit*4 {
			rawLimit = searchMaxLimit * 4
		}
	}
	hits := s.TicketsIdx.Search(vec, vecindex.KindTicketBody, slug, rawLimit)
	if len(hits) == 0 {
		return []TicketHit{}, nil
	}

	colSet := map[domain.Column]struct{}{}
	for _, c := range in.Columns {
		colSet[c] = struct{}{}
	}

	lp.Lock.RLock()
	defer lp.Lock.RUnlock()

	out := make([]TicketHit, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		t, ok := lp.Tickets[h.ID]
		if !ok {
			// Ticket deleted between embed and search; skip.
			continue
		}
		if len(colSet) > 0 {
			if _, allow := colSet[t.Column]; !allow {
				continue
			}
		}
		// Recompute BlockedBy on the cloned copy so callers see fresh state
		// without mutating the cache entry under a read lock.
		cp := cloneTicket(t)
		cp.BlockedBy = computeBlockedBy(cp.DependsOn, lp.Tickets)
		out = append(out, TicketHit{Ticket: cp, Score: h.Score})
	}
	return out, nil
}

// SearchComments runs semantic search over the resident CommentsIdx. When
// ProjectIDOrSlug is set, the index search is owner-filtered to that project's
// slug (cheap & precise). When TicketID is set, hits are post-filtered to that
// ticket's comments only — the index doesn't natively shard by ticket so we
// scan the slice from the cached LoadedProject.
//
// Read-only — no requireSession.
func (s *Service) SearchComments(ctx context.Context, in domain.SearchCommentsInput) ([]CommentHit, error) {
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, fmt.Errorf("%w: query required", domain.ErrInvalidArgument)
	}
	limit := clampSearchLimit(in.Limit)

	// Optional project scoping: load the project so we can both narrow the
	// index search and hydrate comment owners later. When unset, we walk hits
	// across all projects.
	var scopedSlug string
	if strings.TrimSpace(in.ProjectIDOrSlug) != "" {
		lp, _, err := s.Cache.Get(ctx, in.ProjectIDOrSlug)
		if err != nil {
			return nil, err
		}
		scopedSlug = lp.Project.Slug
	}

	vec, err := s.Embed.Embed(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// Over-fetch when post-filtering by ticket id.
	rawLimit := limit
	if strings.TrimSpace(in.TicketID) != "" {
		rawLimit = limit * 4
		if rawLimit > searchMaxLimit*4 {
			rawLimit = searchMaxLimit * 4
		}
	}
	hits := s.CommentsIdx.Search(vec, vecindex.KindComment, scopedSlug, rawLimit)
	if len(hits) == 0 {
		return []CommentHit{}, nil
	}

	// Snapshot the index so we can recover each hit's Owner slug for routing
	// to the right project cache entry (when not already scoped).
	ownerByID := map[string]string{}
	if scopedSlug == "" {
		for _, e := range s.CommentsIdx.Snapshot() {
			ownerByID[e.ID] = e.Owner
		}
	}

	out := make([]CommentHit, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		slug := scopedSlug
		if slug == "" {
			slug = ownerByID[h.ID]
			if slug == "" {
				continue
			}
		}
		hit, ok := s.hydrateCommentHit(ctx, slug, h, in.TicketID)
		if !ok {
			continue
		}
		out = append(out, hit)
	}
	return out, nil
}

// hydrateCommentHit loads the parent project (cached) and returns a CommentHit
// for the given vec hit. Returns ok=false when the comment / project is gone
// or when the optional ticketIDFilter excludes it. Caller does not need to
// hold any lock.
func (s *Service) hydrateCommentHit(ctx context.Context, slug string, h vecindex.Hit, ticketIDFilter string) (CommentHit, bool) {
	lp, _, err := s.Cache.Get(ctx, slug)
	if err != nil {
		return CommentHit{}, false
	}
	lp.Lock.RLock()
	defer lp.Lock.RUnlock()

	// Find the comment in the project's comments map. The map is
	// ticket-id → []*Comment so we walk every ticket. At hobby scale this is
	// trivially cheap.
	for ticketID, cs := range lp.Comments {
		for _, c := range cs {
			if c.ID != h.ID {
				continue
			}
			if ticketIDFilter != "" && ticketID != ticketIDFilter {
				return CommentHit{}, false
			}
			t, ok := lp.Tickets[ticketID]
			if !ok {
				return CommentHit{}, false
			}
			cp := *c
			return CommentHit{
				Comment:     &cp,
				Score:       h.Score,
				TicketTitle: t.Title,
			}, true
		}
	}
	return CommentHit{}, false
}

// SearchLearnings runs semantic search over the resident LearningsIdx (a
// global index — its working set is small enough to keep in memory). The
// optional ProjectIDOrSlug filter is applied post-hoc against the hit's Owner
// (the entry was tagged with the project slug at upsert time).
//
// Read-only — no requireSession.
func (s *Service) SearchLearnings(ctx context.Context, in domain.SearchLearningsInput) ([]LearningHit, error) {
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, fmt.Errorf("%w: query required", domain.ErrInvalidArgument)
	}
	limit := clampSearchLimit(in.Limit)

	vec, err := s.Embed.Embed(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// Resolve the optional project filter to a slug we can compare against
	// each entry's Owner. If the project doesn't exist, fall through with
	// an empty filter rather than fail — caller likely typoed the slug; an
	// empty result is more useful than a 404.
	var scopedSlug string
	if strings.TrimSpace(in.ProjectIDOrSlug) != "" {
		lp, _, err := s.Cache.Get(ctx, in.ProjectIDOrSlug)
		if err != nil {
			return nil, err
		}
		scopedSlug = lp.Project.Slug
	}

	// Over-fetch when post-filtering by project, since the index search runs
	// without an owner filter to keep its single resident shape.
	rawLimit := limit
	if scopedSlug != "" {
		rawLimit = limit * 4
		if rawLimit > searchMaxLimit*4 {
			rawLimit = searchMaxLimit * 4
		}
	}
	hits := s.LearningsIdx.Search(vec, vecindex.KindTicketLearnings, "", rawLimit)
	if len(hits) == 0 {
		return []LearningHit{}, nil
	}

	// Map id → owner so we can scope to a project without re-reading every
	// project off disk on each hit.
	ownerByID := map[string]string{}
	for _, e := range s.LearningsIdx.Snapshot() {
		ownerByID[e.ID] = e.Owner
	}

	out := make([]LearningHit, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		slug := ownerByID[h.ID]
		if slug == "" {
			// Owner unknown (shouldn't happen post-T10 but guard anyway).
			continue
		}
		if scopedSlug != "" && slug != scopedSlug {
			continue
		}
		hit, ok := s.hydrateLearningHit(ctx, slug, h)
		if !ok {
			continue
		}
		out = append(out, hit)
	}
	return out, nil
}

// hydrateLearningHit looks up the ticket inside the named project's cache
// entry and builds a LearningHit. ok=false when the project or ticket no
// longer exists, or when the ticket isn't actually completed (defensive — the
// learnings index should only carry completed-ticket entries, but a stale
// in-memory entry from before a delete is possible).
func (s *Service) hydrateLearningHit(ctx context.Context, slug string, h vecindex.Hit) (LearningHit, bool) {
	lp, _, err := s.Cache.Get(ctx, slug)
	if err != nil {
		return LearningHit{}, false
	}
	lp.Lock.RLock()
	defer lp.Lock.RUnlock()

	t, ok := lp.Tickets[h.ID]
	if !ok {
		return LearningHit{}, false
	}
	if t.Learnings == nil {
		return LearningHit{}, false
	}
	var completedAt time.Time
	if t.CompletedAt != nil {
		completedAt = *t.CompletedAt
	}
	return LearningHit{
		TicketID:    t.ID,
		ProjectID:   lp.Project.ID,
		ProjectSlug: lp.Project.Slug,
		Title:       t.Title,
		Learnings:   *t.Learnings,
		Score:       h.Score,
		CompletedAt: completedAt,
	}, true
}

// clampSearchLimit applies the SPEC's default-10/cap-50 rule to a caller-
// supplied limit. Mirrored on each search method's edge so the cap is part of
// the public API contract independent of the underlying vecindex defaults.
func clampSearchLimit(limit int) int {
	if limit <= 0 {
		return searchDefaultLimit
	}
	if limit > searchMaxLimit {
		return searchMaxLimit
	}
	return limit
}
