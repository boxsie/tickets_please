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
	"sort"
	"strings"
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/embed"
	"tickets_please/internal/store"
	"tickets_please/internal/vecindex"
)

// indexKind selects which of a mount's four resident indexes
// mountProviderAndIndex returns. Lives here rather than vecindex.Kind because
// per-mount routing also touches the embed.Provider, which Kind does not.
type indexKind int

const (
	indexKindSummaries indexKind = iota
	indexKindTickets
	indexKindLearnings
	indexKindComments
)

// mountProviderAndIndex returns the (embed.Provider, *vecindex.Index) pair
// the search RPCs use for project-scoped queries. Walks the mount registry;
// falls back to s.Embed + s.defaultIndexes when the slug isn't mounted (the
// stdio bootstrap path). Never returns nil pointers — defaults are filled.
func (s *Service) mountProviderAndIndex(slug string, kind indexKind) (embed.Provider, *vecindex.Index) {
	provider := s.Embed
	var idx *vecindex.Index
	s.mountsMu.Lock()
	if mount, ok := s.projectMounts[slug]; ok && mount != nil {
		if mount.Embed != nil {
			provider = mount.Embed
		}
		switch kind {
		case indexKindSummaries:
			idx = mount.SummaryIdx
		case indexKindTickets:
			idx = mount.TicketsIdx
		case indexKindLearnings:
			idx = mount.LearningsIdx
		case indexKindComments:
			idx = mount.CommentsIdx
		}
	}
	s.mountsMu.Unlock()
	if idx == nil {
		switch kind {
		case indexKindSummaries:
			idx = s.defaultIndexes.Summaries
		case indexKindTickets:
			idx = s.defaultIndexes.Tickets
		case indexKindLearnings:
			idx = s.defaultIndexes.Learnings
		case indexKindComments:
			idx = s.defaultIndexes.Comments
		}
	}
	return provider, idx
}

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

// SearchProjects runs semantic search over each mounted project's per-mount
// SummaryIdx. Per-mount dims may differ (each project carries its own embed
// model), so the query is embedded once per distinct provider/mount and the
// resulting hits are merged + re-sorted by score.
//
// Phase summaries share each mount's SummaryIdx; they're filtered out by
// id-shape (only project ids appear in the per-mount id→slug map).
//
// Read-only — no requireSession.
func (s *Service) SearchProjects(ctx context.Context, query string, limit int) ([]ProjectHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("%w: query required", domain.ErrInvalidArgument)
	}
	limit = clampSearchLimit(limit)
	rawLimit := limit * 2
	if rawLimit > searchMaxLimit*2 {
		rawLimit = searchMaxLimit * 2
	}

	// Snapshot mounts (slug + provider + index + store).
	type sumSrc struct {
		slug     string
		provider embed.Provider
		idx      *vecindex.Index
		st       *store.Store
	}
	var sources []sumSrc
	_ = s.WalkProjectMounts(func(slug string, mount *ProjectMount) error {
		if mount == nil || mount.Store == nil || mount.SummaryIdx == nil {
			return nil
		}
		p := mount.Embed
		if p == nil {
			p = s.Embed
		}
		sources = append(sources, sumSrc{slug: slug, provider: p, idx: mount.SummaryIdx, st: mount.Store})
		return nil
	})
	// Stdio fallback: registry empty + default Store + default index. Built
	// once at startup so single-project tests/CLI work without registering.
	if len(sources) == 0 && s.Store != nil && s.defaultIndexes.Summaries != nil {
		sources = append(sources, sumSrc{
			slug: "", provider: s.Embed, idx: s.defaultIndexes.Summaries, st: s.Store,
		})
	}

	type scored struct {
		hit  vecindex.Hit
		slug string
	}
	var pool []scored
	idToSlug := make(map[string]string)
	for _, src := range sources {
		// Resolve slug-from-store for the stdio-fallback empty-slug case so
		// idToSlug below still works.
		_ = src.st.WalkProjects(func(slug string, rec *store.ProjectRecord) error {
			projSlug := src.slug
			if projSlug == "" {
				projSlug = slug
			}
			idToSlug[rec.ID] = projSlug
			return nil
		})
		vec, err := src.provider.Embed(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("embed query (slug=%s): %w", src.slug, err)
		}
		hits := src.idx.Search(vec, vecindex.KindProjectSummary, "", rawLimit)
		for _, h := range hits {
			projSlug := src.slug
			if projSlug == "" {
				projSlug = idToSlug[h.ID]
			}
			pool = append(pool, scored{hit: h, slug: projSlug})
		}
	}
	if len(pool) == 0 {
		return []ProjectHit{}, nil
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].hit.Score != pool[j].hit.Score {
			return pool[i].hit.Score > pool[j].hit.Score
		}
		return pool[i].hit.ID < pool[j].hit.ID
	})

	out := make([]ProjectHit, 0, limit)
	for _, sh := range pool {
		if len(out) >= limit {
			break
		}
		slug := sh.slug
		if slug == "" {
			slug = idToSlug[sh.hit.ID]
		}
		if slug == "" {
			// Phase hit (or project deleted between embed write and search).
			continue
		}
		p, err := s.GetProject(ctx, slug)
		if err != nil {
			continue
		}
		out = append(out, ProjectHit{Project: p, ProjectSlug: slug, Score: sh.hit.Score})
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

	provider, idx := s.mountProviderAndIndex(slug, indexKindTickets)
	vec, err := provider.Embed(ctx, q)
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
	hits := idx.Search(vec, vecindex.KindTicketBody, slug, rawLimit)
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

	// Over-fetch when post-filtering by ticket id.
	rawLimit := limit
	if strings.TrimSpace(in.TicketID) != "" {
		rawLimit = limit * 4
		if rawLimit > searchMaxLimit*4 {
			rawLimit = searchMaxLimit * 4
		}
	}

	// Project-scoped path: use the mount's provider + its CommentsIdx.
	if strings.TrimSpace(in.ProjectIDOrSlug) != "" {
		lp, _, err := s.Cache.Get(ctx, in.ProjectIDOrSlug)
		if err != nil {
			return nil, err
		}
		slug := lp.Project.Slug
		provider, idx := s.mountProviderAndIndex(slug, indexKindComments)
		vec, err := provider.Embed(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("embed query: %w", err)
		}
		hits := idx.Search(vec, vecindex.KindComment, slug, rawLimit)
		out := make([]CommentHit, 0, limit)
		for _, h := range hits {
			if len(out) >= limit {
				break
			}
			hit, ok := s.hydrateCommentHit(ctx, slug, h, in.TicketID)
			if !ok {
				continue
			}
			out = append(out, hit)
		}
		return out, nil
	}

	// Unscoped path: aggregate across mounts. Per-mount dims may differ, so
	// embed once per mount-provider and merge hits by score. Falls back to
	// the registry-empty defaultIndexes when no mounts exist.
	type src struct {
		slug     string
		provider embed.Provider
		idx      *vecindex.Index
	}
	var sources []src
	_ = s.WalkProjectMounts(func(slug string, mount *ProjectMount) error {
		if mount == nil || mount.CommentsIdx == nil {
			return nil
		}
		p := mount.Embed
		if p == nil {
			p = s.Embed
		}
		sources = append(sources, src{slug: slug, provider: p, idx: mount.CommentsIdx})
		return nil
	})
	if len(sources) == 0 && s.defaultIndexes.Comments != nil {
		sources = append(sources, src{slug: "", provider: s.Embed, idx: s.defaultIndexes.Comments})
	}
	type scored struct {
		hit  vecindex.Hit
		slug string
	}
	var pool []scored
	for _, sr := range sources {
		vec, err := sr.provider.Embed(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("embed query (slug=%s): %w", sr.slug, err)
		}
		// In the registry-empty stdio path, sr.slug is "" and the index entry
		// Owner carries the slug, so don't filter by owner. When sr.slug is
		// set, scope the search to that mount's slug as a defence against
		// crossed entries (per-mount index should only hold its own slug).
		hits := sr.idx.Search(vec, vecindex.KindComment, sr.slug, rawLimit)
		for _, h := range hits {
			pool = append(pool, scored{hit: h, slug: sr.slug})
		}
	}
	if len(pool) == 0 {
		return []CommentHit{}, nil
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].hit.Score != pool[j].hit.Score {
			return pool[i].hit.Score > pool[j].hit.Score
		}
		return pool[i].hit.ID < pool[j].hit.ID
	})
	// For the stdio fallback (slug=""), recover Owner via the index Snapshot.
	ownerByID := map[string]string{}
	if s.defaultIndexes.Comments != nil {
		for _, e := range s.defaultIndexes.Comments.Snapshot() {
			ownerByID[e.ID] = e.Owner
		}
	}
	out := make([]CommentHit, 0, limit)
	for _, sh := range pool {
		if len(out) >= limit {
			break
		}
		slug := sh.slug
		if slug == "" {
			slug = ownerByID[sh.hit.ID]
		}
		if slug == "" {
			continue
		}
		hit, ok := s.hydrateCommentHit(ctx, slug, sh.hit, in.TicketID)
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

// SearchLearnings runs semantic search across mounted projects' per-mount
// LearningsIdx. Each mount may use a different embedder/dim, so the query is
// embedded once per provider and the resulting hits are merged by score.
// The optional ProjectIDOrSlug filter scopes to one mount.
//
// Read-only — no requireSession.
func (s *Service) SearchLearnings(ctx context.Context, in domain.SearchLearningsInput) ([]LearningHit, error) {
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, fmt.Errorf("%w: query required", domain.ErrInvalidArgument)
	}
	limit := clampSearchLimit(in.Limit)
	rawLimit := limit
	if rawLimit > searchMaxLimit*4 {
		rawLimit = searchMaxLimit * 4
	}

	var scopedSlug string
	if strings.TrimSpace(in.ProjectIDOrSlug) != "" {
		lp, _, err := s.Cache.Get(ctx, in.ProjectIDOrSlug)
		if err != nil {
			return nil, err
		}
		scopedSlug = lp.Project.Slug
	}

	// Snapshot mounts (filtered to scopedSlug if set).
	type src struct {
		slug     string
		provider embed.Provider
		idx      *vecindex.Index
	}
	var sources []src
	_ = s.WalkProjectMounts(func(slug string, mount *ProjectMount) error {
		if mount == nil || mount.LearningsIdx == nil {
			return nil
		}
		if scopedSlug != "" && slug != scopedSlug {
			return nil
		}
		p := mount.Embed
		if p == nil {
			p = s.Embed
		}
		sources = append(sources, src{slug: slug, provider: p, idx: mount.LearningsIdx})
		return nil
	})
	// Stdio fallback: registry-empty + scoped → no-op (scoped to a slug we
	// can't resolve). Otherwise consult defaultIndexes.
	if len(sources) == 0 && scopedSlug == "" && s.defaultIndexes.Learnings != nil {
		sources = append(sources, src{slug: "", provider: s.Embed, idx: s.defaultIndexes.Learnings})
	}

	type scored struct {
		hit  vecindex.Hit
		slug string
	}
	var pool []scored
	for _, sr := range sources {
		vec, err := sr.provider.Embed(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("embed query (slug=%s): %w", sr.slug, err)
		}
		// Per-mount index already only holds this slug's entries; passing
		// owner=sr.slug is defence-in-depth. The stdio fallback path uses
		// owner="" (snapshot lookup below recovers slug from Owner field).
		hits := sr.idx.Search(vec, vecindex.KindTicketLearnings, sr.slug, rawLimit)
		for _, h := range hits {
			pool = append(pool, scored{hit: h, slug: sr.slug})
		}
	}
	if len(pool) == 0 {
		return []LearningHit{}, nil
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].hit.Score != pool[j].hit.Score {
			return pool[i].hit.Score > pool[j].hit.Score
		}
		return pool[i].hit.ID < pool[j].hit.ID
	})
	// stdio-fallback owner recovery
	ownerByID := map[string]string{}
	if s.defaultIndexes.Learnings != nil {
		for _, e := range s.defaultIndexes.Learnings.Snapshot() {
			ownerByID[e.ID] = e.Owner
		}
	}
	out := make([]LearningHit, 0, limit)
	for _, sh := range pool {
		if len(out) >= limit {
			break
		}
		slug := sh.slug
		if slug == "" {
			slug = ownerByID[sh.hit.ID]
		}
		if slug == "" {
			continue
		}
		hit, ok := s.hydrateLearningHit(ctx, slug, sh.hit)
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
