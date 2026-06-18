package svc

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/cache"
	"tickets_please/internal/domain"
	"tickets_please/internal/eventbus"
	"tickets_please/internal/store"
	"tickets_please/internal/worker"
)

// listTicketsDefaultLimit / listTicketsMaxLimit cap pagination per T05 SPEC.
const (
	listTicketsDefaultLimit = 50
	listTicketsMaxLimit     = 200
)

// CreateTicket lands a new ticket in `todo`, atomically writing
// `ticket.yaml` + `body.md` under the project's flock. PhaseIDOrSlug, when
// supplied, must match an existing phase on the project; depends_on /
// parallelizable_with must reference tickets in the same project. The
// returned *domain.Ticket carries the freshly-computed BlockedBy.
func (s *Service) CreateTicket(ctx context.Context, in domain.CreateTicketInput) (*domain.Ticket, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}

	if err := requireNonEmptyTrimmed("title", in.Title); err != nil {
		return nil, err
	}
	title := normalizeLabel(in.Title)
	if in.Wave < 0 {
		return nil, fmt.Errorf("%w: wave must be >= 0", domain.ErrInvalidArgument)
	}

	lp, _, err := s.Cache.Get(ctx, in.ProjectIDOrSlug)
	if err != nil {
		return nil, err
	}

	// Acting-for agents must have membership on this project (key-only agents
	// are unrestricted — no-op).
	if err := s.authorizeActingFor(agent, lp.Project.ID, true); err != nil {
		return nil, err
	}

	// Resolve the host store for this project once — every BeginOp / path
	// build below must target it, not s.Store, so per-repo mounts write back
	// to the correct on-disk location.
	st, err := s.ResolveProjectStore(ctx, lp.Project.Slug)
	if err != nil {
		return nil, err
	}

	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	// Validate phase, if any. Accept either phase id or slug. The cache
	// hydrates phases up front, so the on-disk dir name is reconstructible
	// from PhaseRecord.Number + PhaseRecord.Slug — see SPEC §Phases / file
	// layout.
	var phaseIDPtr *string
	var phaseDirName string
	if in.PhaseIDOrSlug != nil {
		ph, ok := resolvePhase(lp, *in.PhaseIDOrSlug)
		if !ok {
			return nil, fmt.Errorf("%w: phase %q not found in project", domain.ErrNotFound, *in.PhaseIDOrSlug)
		}
		phaseIDPtr = &ph.ID
		phaseDirName = fmt.Sprintf("%03d-%s", ph.Number, ph.Slug)
	}

	// Cross-project dep refs are rejected by checking against the cached
	// ticket map. Anything not found is treated as cross-project (or just
	// nonexistent).
	for _, dep := range in.DependsOn {
		dt, ok := lp.Tickets[dep]
		if !ok {
			return nil, fmt.Errorf("%w: depends_on ticket %q not in project", domain.ErrInvalidArgument, dep)
		}
		if dt.Kind == domain.KindIdea {
			return nil, fmt.Errorf("%w: depends_on ticket %q is an idea; ideas can't gate work — promote it first with promote_idea", domain.ErrInvalidArgument, dep)
		}
	}
	for _, par := range in.ParallelizableWith {
		pt, ok := lp.Tickets[par]
		if !ok {
			return nil, fmt.Errorf("%w: parallelizable_with ticket %q not in project", domain.ErrInvalidArgument, par)
		}
		if pt.Kind == domain.KindIdea {
			return nil, fmt.Errorf("%w: parallelizable_with ticket %q is an idea; promote it first with promote_idea", domain.ErrInvalidArgument, par)
		}
	}

	// Project-global ticket numbering. We scan disk because the cache
	// hydrates tickets without their on-disk Number — and deletions can
	// leave gaps, so max+1 is the only safe answer.
	number, err := s.nextTicketNumber(st, lp.Project.Slug)
	if err != nil {
		return nil, err
	}

	dirName, err := s.uniqueTicketDirName(st, lp.Project.Slug, number, title)
	if err != nil {
		return nil, err
	}

	// Pick the on-disk path: phased vs phase-less.
	var ticketDirRel string
	if phaseDirName != "" {
		ticketDirRel = filepath.Join("phases", phaseDirName, "tickets", dirName)
	} else {
		ticketDirRel = filepath.Join("tickets", dirName)
	}

	now := time.Now()
	rec := &store.TicketRecord{
		ID:                 uuid.NewString(),
		ProjectID:          lp.Project.ID,
		Number:             number,
		Title:              title,
		Column:             domain.ColumnTodo,
		Kind:               in.Kind.Stored(),
		PhaseID:            phaseIDPtr,
		Wave:               in.Wave,
		DependsOn:          append([]string(nil), in.DependsOn...),
		ParallelizableWith: append([]string(nil), in.ParallelizableWith...),
		CreatedByAgentID:   &agent.ID,
		CreatedForUserID:   actingForUserID(agent),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	yamlBytes, err := store.MarshalYAML(rec)
	if err != nil {
		return nil, err
	}

	op, err := st.BeginOp()
	if err != nil {
		return nil, err
	}
	defer op.Abort()
	if err := op.Write(filepath.Join(ticketDirRel, "ticket.yaml"), yamlBytes); err != nil {
		return nil, err
	}
	if err := op.Write(filepath.Join(ticketDirRel, "body.md"), []byte(ensureTrailingNewline(in.Body))); err != nil {
		return nil, err
	}
	caption := fmt.Sprintf("create ticket %s/%03d", lp.Project.Slug, number)
	if err := op.Commit(ctx, store.LockProject(lp.Project.Slug), agent, caption); err != nil {
		return nil, fmt.Errorf("commit create ticket: %w", err)
	}

	// Async embed: title + body → mount's TicketsIdx.
	if mount := s.mountForSlug(lp.Project.Slug); mount != nil && mount.Worker != nil {
		ticketDirAbs := filepath.Join(st.Root, ticketDirRel)
		mount.Worker.Enqueue(worker.Job{
			Kind:        worker.JobTicketBody,
			SourcePath:  filepath.Join(ticketDirAbs, "body.md"),
			SidecarPath: filepath.Join(ticketDirAbs, "body.embedding.json"),
			EntryID:     rec.ID,
			Owner:       lp.Project.Slug,
			Text:        title + "\n\n" + in.Body,
		})
	}

	t := &domain.Ticket{
		ID:                 rec.ID,
		ProjectID:          rec.ProjectID,
		Title:              rec.Title,
		Body:               in.Body,
		Column:             rec.Column,
		Kind:               rec.Kind.OrWork(),
		PhaseID:            rec.PhaseID,
		Wave:               rec.Wave,
		DependsOn:          append([]string(nil), rec.DependsOn...),
		ParallelizableWith: append([]string(nil), rec.ParallelizableWith...),
		CreatedBy:          &domain.AgentRef{ID: agent.ID, Name: agent.Name},
		CreatedFor:         actingForRef(agent),
		CreatedAt:          rec.CreatedAt,
		UpdatedAt:          rec.UpdatedAt,
	}
	t.BlockedBy = computeBlockedBy(t.DependsOn, lp.Tickets)

	// Insert into the cached map so subsequent reads see it without waiting
	// for fsnotify. Comments map gets an empty slot.
	lp.Tickets[t.ID] = t
	if _, ok := lp.Comments[t.ID]; !ok {
		lp.Comments[t.ID] = nil
	}

	s.publish(withActor(eventbus.Event{
		Kind:      eventbus.KindTicketCreated,
		Topics:    ticketTopics(t.ID, lp.Project.ID, t.PhaseID),
		TicketID:  t.ID,
		ProjectID: lp.Project.ID,
		PhaseID:   derefStr(t.PhaseID),
		ToColumn:  string(t.Column),
	}, agent))

	// Return a copy so callers can't mutate cached state without a lock.
	cp := *t
	cp.DependsOn = append([]string(nil), t.DependsOn...)
	cp.ParallelizableWith = append([]string(nil), t.ParallelizableWith...)
	cp.BlockedBy = append([]string(nil), t.BlockedBy...)
	return &cp, nil
}

// GetTicket returns a hydrated ticket by id. v1 walks projects on disk to
// find the ticket's host project, then loads that one project through the
// cache. ListTickets is the structured per-project read path; this is the
// "I have an opaque id from somewhere" escape hatch.
func (s *Service) GetTicket(ctx context.Context, id string) (*domain.Ticket, error) {
	if err := requireNonEmptyTrimmed("ticket id", id); err != nil {
		return nil, err
	}

	_, hostSlug, err := s.hostStoreForTicket(id)
	if err != nil {
		return nil, err
	}

	lp, _, err := s.Cache.Get(ctx, hostSlug)
	if err != nil {
		return nil, err
	}
	lp.Lock.RLock()
	defer lp.Lock.RUnlock()
	t, ok := lp.Tickets[id]
	if !ok {
		return nil, fmt.Errorf("%w: ticket %s", domain.ErrNotFound, id)
	}
	t.BlockedBy = computeBlockedBy(t.DependsOn, lp.Tickets)
	return cloneTicket(t), nil
}

// ResolveTicketRef resolves a human-facing ticket reference to its UUID so the
// ticket-targeting tools can accept the shortcode agents actually see (the
// global ticket number that shows up in commit messages and conversation)
// instead of forcing the opaque UUID. ref may be:
//
//   - a UUID or any other opaque id — returned unchanged; existence is checked
//     by the downstream operation, so this stays a pure passthrough.
//   - "<project-slug>/<number>" e.g. "tickets-please/76" or "tickets-please/076"
//     — resolved within the named project regardless of the bound session.
//   - a bare "<number>" e.g. "76" — resolved within defaultSlug, which callers
//     supply from the session's bound project; errors if defaultSlug is empty.
//   - a truncated UUID prefix e.g. "31ca06c1" (the id[:8] stub the web UI shows
//     and agents copy into memory), optionally as "<slug>/<prefix>" — resolved
//     to the unique ticket whose UUID starts with it. This is a best-effort
//     backstop: an unmatched prefix falls back to passthrough, an ambiguous one
//     errors. See resolveTicketPrefix.
//
// Resolution walks the host store's ticket records by Number (domain.Ticket
// carries no number; only the store record + on-disk dir name do). O(tickets)
// is fine at this scale. A missing shortcode yields an actionable error naming
// the slug + number rather than a generic not-found.
func (s *Service) ResolveTicketRef(ctx context.Context, defaultSlug, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("%w: ticket reference is empty", domain.ErrInvalidArgument)
	}

	slug, numStr, isShortcode := parseTicketShortcode(ref, defaultSlug)
	if !isShortcode {
		// Not a number-shortcode. It may be a full opaque id (UUID etc.) or a
		// truncated UUID prefix an agent copied from the UI/a log. Try the
		// prefix backstop; on no match (or no resolvable project) fall through
		// to passthrough so opaque ids reach the downstream existence check.
		if id, ok, err := s.resolveTicketPrefix(ctx, defaultSlug, ref); err != nil {
			return "", err
		} else if ok {
			return id, nil
		}
		return ref, nil
	}
	if slug == "" {
		return "", fmt.Errorf("%w: ticket number %q has no project — bind a project via register_agent or use the \"<project-slug>/%s\" form", domain.ErrInvalidArgument, numStr, numStr)
	}
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return "", fmt.Errorf("%w: %q is not a valid ticket number", domain.ErrInvalidArgument, numStr)
	}

	st, err := s.ResolveProjectStore(ctx, slug)
	if err != nil {
		return "", err
	}
	var found string
	walkErr := st.WalkTickets(slug, func(_, _ string, tr *store.TicketRecord) error {
		if tr.Number == num && found == "" {
			found = tr.ID
		}
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("walk tickets: %w", walkErr)
	}
	if found == "" {
		return "", fmt.Errorf("%w: no ticket %s/%d in project %s", domain.ErrNotFound, slug, num, slug)
	}
	return found, nil
}

// parseTicketShortcode classifies a ticket reference. It reports isShortcode
// false for anything that isn't a bare number or "<slug>/<number>" (i.e. an
// opaque UUID), leaving slug/numStr empty. A bare number adopts defaultSlug.
// Project slugs are single-segment and URL-safe (no "/"), so splitting on the
// last "/" is unambiguous.
func parseTicketShortcode(ref, defaultSlug string) (slug, numStr string, isShortcode bool) {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		s, n := ref[:i], ref[i+1:]
		if s != "" && isAllDigits(n) {
			return s, n, true
		}
		return "", "", false
	}
	if isAllDigits(ref) {
		return defaultSlug, ref, true
	}
	return "", "", false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// resolveTicketPrefix is the best-effort UUID-prefix backstop for
// ResolveTicketRef. ref is the id[:8]-style stub the web UI renders (see
// internal/web/components/pages/tickets/props.go) and agents tend to copy into
// memory, optionally as "<slug>/<prefix>". It reports:
//
//   - (id, true, nil)   — exactly one ticket UUID in the project starts with the prefix
//   - ("", false, nil)  — ref isn't a plausible prefix, the project can't be
//     resolved, or nothing matched; caller falls back to passthrough
//   - ("", false, err)  — the prefix is ambiguous (>1 match): ErrInvalidArgument
//
// Scope is the slug embedded in ref, else defaultSlug. The prefix must be pure
// hex with at least one hex letter (pure-digit refs are number-shortcodes,
// handled before we get here) and shorter than a full dashed UUID, so real
// UUIDs and arbitrary opaque ids never reach the walk — they stay passthrough.
func (s *Service) resolveTicketPrefix(ctx context.Context, defaultSlug, ref string) (string, bool, error) {
	slug := defaultSlug
	prefix := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		if ref[:i] != "" {
			slug = ref[:i]
		}
		prefix = ref[i+1:]
	}
	if slug == "" || !isUUIDPrefix(prefix) {
		return "", false, nil
	}

	st, err := s.ResolveProjectStore(ctx, slug)
	if err != nil {
		// Unknown/unbound project — not a hard error here; let the ref pass
		// through so a genuine opaque id still reaches its downstream check.
		return "", false, nil
	}
	var ids []string
	walkErr := st.WalkTickets(slug, func(_, _ string, tr *store.TicketRecord) error {
		ids = append(ids, tr.ID)
		return nil
	})
	if walkErr != nil {
		return "", false, fmt.Errorf("walk tickets: %w", walkErr)
	}
	matches := matchPrefix(ids, prefix)
	switch len(matches) {
	case 0:
		return "", false, nil
	case 1:
		return matches[0], true, nil
	default:
		return "", false, fmt.Errorf("%w: ticket id prefix %q is ambiguous in project %s (%d matches) — use the full UUID or the <slug>/<number> shortcode", domain.ErrInvalidArgument, prefix, slug, len(matches))
	}
}

// matchPrefix returns the ids that start with prefix (case-insensitive).
// Pure so the unique/none/ambiguous decision is testable with crafted ids —
// random UUIDs effectively never collide on a 4+ hex prefix in a live store.
func matchPrefix(ids []string, prefix string) []string {
	lower := strings.ToLower(prefix)
	var out []string
	for _, id := range ids {
		if strings.HasPrefix(strings.ToLower(id), lower) {
			out = append(out, id)
		}
	}
	return out
}

// isUUIDPrefix reports whether s is a plausible truncated UUID: pure hex,
// 4..31 chars, with at least one a–f letter so a pure-digit ref stays a
// number-shortcode. A full UUID (36 chars, dashes) fails the length/charset
// test and is left to pass through untouched.
func isUUIDPrefix(s string) bool {
	if len(s) < 4 || len(s) > 31 {
		return false
	}
	hasLetter := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
			hasLetter = true
		default:
			return false
		}
	}
	return hasLetter
}

// ListTickets returns a filtered, paginated slice of tickets in a project.
// Cursor format: base64(`<rfc3339-created-at>|<id>`). Decode failures surface
// as ErrInvalidArgument. Order: Wave asc with wave 0 sorted last; tie-break
// by CreatedAt asc.
func (s *Service) ListTickets(ctx context.Context, in domain.ListTicketsInput) ([]*domain.Ticket, string, error) {
	lp, _, err := s.Cache.Get(ctx, in.ProjectIDOrSlug)
	if err != nil {
		return nil, "", err
	}
	lp.Lock.RLock()
	defer lp.Lock.RUnlock()

	limit := in.Limit
	if limit <= 0 {
		limit = listTicketsDefaultLimit
	}
	if limit > listTicketsMaxLimit {
		limit = listTicketsMaxLimit
	}

	var afterCreated time.Time
	var afterID string
	if in.Cursor != "" {
		c, id, err := decodeCursor(in.Cursor)
		if err != nil {
			return nil, "", err
		}
		afterCreated, afterID = c, id
	}

	out := make([]*domain.Ticket, 0)
	for _, t := range lp.Tickets {
		if t.Archived && !in.IncludeArchived {
			continue
		}
		if t.Kind == domain.KindIdea && !in.IncludeIdeas {
			continue
		}
		if in.Column != nil && t.Column != *in.Column {
			continue
		}
		if !phaseFilterMatches(t, in.PhaseIDOrSlug, lp) {
			continue
		}
		if in.Wave != nil && t.Wave != *in.Wave {
			continue
		}
		// Recompute BlockedBy against the current ticket map so reads
		// reflect ongoing column moves without requiring a reload.
		t.BlockedBy = computeBlockedBy(t.DependsOn, lp.Tickets)
		if in.ReadyOnly {
			// Ideas are never "ready work" — they must be promoted first.
			// Guard here too so ready_only stays correct even if a caller
			// passes IncludeIdeas alongside it.
			if t.Kind == domain.KindIdea {
				continue
			}
			if len(t.BlockedBy) > 0 {
				continue
			}
			if t.Column != domain.ColumnTodo && t.Column != domain.ColumnInProgress {
				continue
			}
		}
		out = append(out, t)
	}

	sort.Slice(out, func(i, j int) bool { return ticketLess(out[i], out[j]) })

	// Apply cursor: drop entries up to and including the cursor's anchor.
	if !afterCreated.IsZero() || afterID != "" {
		idx := 0
		for ; idx < len(out); idx++ {
			if out[idx].CreatedAt.Equal(afterCreated) && out[idx].ID == afterID {
				idx++
				break
			}
		}
		out = out[idx:]
	}

	nextCursor := ""
	if len(out) > limit {
		last := out[limit-1]
		nextCursor = encodeCursor(last.CreatedAt, last.ID)
		out = out[:limit]
	}

	// Clone every ticket so callers can't mutate the cache without a lock.
	cp := make([]*domain.Ticket, len(out))
	for i, t := range out {
		cp[i] = cloneTicket(t)
	}
	return cp, nextCursor, nil
}

// UpdateTicket mutates title / body / wave / dependency edges on an existing
// ticket. Column and phase are owned by MoveTicket / AssignTicketToPhase.
func (s *Service) UpdateTicket(ctx context.Context, id string, in domain.UpdateTicketInput) (*domain.Ticket, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireNonEmptyTrimmed("ticket id", id); err != nil {
		return nil, err
	}
	if in.Wave != nil && *in.Wave < 0 {
		return nil, fmt.Errorf("%w: wave must be >= 0", domain.ErrInvalidArgument)
	}

	// Find the host project + store. Reuse GetTicket's resolution machinery —
	// but return ErrNotFound on miss without lazy-loading every project. The
	// store returned is the one we'll write back to (per-repo mounts vs the
	// default central store).
	st, hostSlug, err := s.hostStoreForTicket(id)
	if err != nil {
		return nil, err
	}
	lp, _, err := s.Cache.Get(ctx, hostSlug)
	if err != nil {
		return nil, err
	}

	if err := s.authorizeActingFor(agent, lp.Project.ID, true); err != nil {
		return nil, err
	}

	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	t, ok := lp.Tickets[id]
	if !ok {
		return nil, fmt.Errorf("%w: ticket %s", domain.ErrNotFound, id)
	}

	titleChanged := false
	bodyChanged := false
	newTitle := t.Title
	newBody := t.Body
	newWave := t.Wave
	newDependsOn := append([]string(nil), t.DependsOn...)
	newParallelizableWith := append([]string(nil), t.ParallelizableWith...)
	if in.Title != nil {
		nt := normalizeLabel(*in.Title)
		if nt == "" {
			return nil, fmt.Errorf("%w: title cannot be blanked", domain.ErrInvalidArgument)
		}
		if nt != t.Title {
			newTitle = nt
			titleChanged = true
		}
	}
	if in.Body != nil {
		if *in.Body != t.Body {
			newBody = *in.Body
			bodyChanged = true
		}
	}
	if in.Wave != nil {
		newWave = *in.Wave
	}
	if in.DependsOn != nil {
		if err := validateTicketRefs(lp, id, "depends_on", *in.DependsOn); err != nil {
			return nil, err
		}
		if dependencyUpdateCreatesCycle(lp, id, *in.DependsOn) {
			return nil, fmt.Errorf("%w: depends_on would create a dependency cycle for ticket %s", domain.ErrInvalidArgument, id)
		}
		newDependsOn = compactTicketRefs(*in.DependsOn)
	}
	if in.ParallelizableWith != nil {
		if err := validateTicketRefs(lp, id, "parallelizable_with", *in.ParallelizableWith); err != nil {
			return nil, err
		}
		newParallelizableWith = compactTicketRefs(*in.ParallelizableWith)
	}

	// Re-read the on-disk record so we don't drop fields the cache doesn't
	// model (e.g. CompletedByAgentID after a T07 lands and then this T05
	// path runs concurrently).
	ticketDirRel, ticketDirAbs, err := s.findTicketDir(st, lp.Project.Slug, id)
	if err != nil {
		return nil, err
	}
	rec := &store.TicketRecord{}
	if err := store.ReadYAML(filepath.Join(ticketDirAbs, "ticket.yaml"), rec); err != nil {
		return nil, fmt.Errorf("read ticket: %w", err)
	}
	rec.Title = newTitle
	rec.Wave = newWave
	rec.DependsOn = append([]string(nil), newDependsOn...)
	rec.ParallelizableWith = append([]string(nil), newParallelizableWith...)
	rec.UpdatedAt = time.Now()

	yamlBytes, err := store.MarshalYAML(rec)
	if err != nil {
		return nil, err
	}

	op, err := st.BeginOp()
	if err != nil {
		return nil, err
	}
	defer op.Abort()
	if err := op.Write(filepath.Join(ticketDirRel, "ticket.yaml"), yamlBytes); err != nil {
		return nil, err
	}
	if bodyChanged {
		if err := op.Write(filepath.Join(ticketDirRel, "body.md"), []byte(ensureTrailingNewline(newBody))); err != nil {
			return nil, err
		}
	}
	caption := fmt.Sprintf("update ticket %s/%03d", lp.Project.Slug, rec.Number)
	if err := op.Commit(ctx, store.LockProject(lp.Project.Slug), agent, caption); err != nil {
		return nil, fmt.Errorf("commit update ticket: %w", err)
	}

	if titleChanged || bodyChanged {
		if mount := s.mountForSlug(lp.Project.Slug); mount != nil && mount.Worker != nil {
			ticketDirAbs := filepath.Join(st.Root, ticketDirRel)
			mount.Worker.Enqueue(worker.Job{
				Kind:        worker.JobTicketBody,
				SourcePath:  filepath.Join(ticketDirAbs, "body.md"),
				SidecarPath: filepath.Join(ticketDirAbs, "body.embedding.json"),
				EntryID:     rec.ID,
				Owner:       lp.Project.Slug,
				Text:        newTitle + "\n\n" + newBody,
			})
		}
	}

	// Apply mutations to the cached ticket in place. Lock is held.
	t.Title = newTitle
	if bodyChanged {
		t.Body = newBody
	}
	t.Wave = newWave
	t.DependsOn = append([]string(nil), newDependsOn...)
	t.ParallelizableWith = append([]string(nil), newParallelizableWith...)
	t.BlockedBy = computeBlockedBy(t.DependsOn, lp.Tickets)
	t.UpdatedAt = rec.UpdatedAt

	cp := cloneTicket(t)
	cp.BlockedBy = computeBlockedBy(cp.DependsOn, lp.Tickets)
	return cp, nil
}

// completionMinLen is the SPEC-mandated minimum length (after trim) for the
// `learnings` field on CompleteTicket. The other two completion fields
// (testing_evidence, work_summary) are optional and accept empty.
const completionMinLen = 10

// MoveTicket transitions a ticket between columns under the project's flock.
// Every move requires a non-empty comment (audit trail) and rejects ColumnDone
// targets — done is reachable only via CompleteTicket, and once-done tickets
// cannot be reopened.
//
// Dependency enforcement only fires when target == ColumnInProgress: a
// non-empty BlockedBy with cfg.EnforceDependencies=true returns
// ErrFailedPrecondition; with enforcement off, we log a warning and prepend
// "⚠ moved with unmet deps: [...]" to the move comment body so the audit
// trail records the policy choice.
//
// Both the updated ticket.yaml and the new system_move comment file are
// written via a single StageOp, so a partial state can never be observed:
// either both land or neither does (per SPEC §Atomicity).
func (s *Service) MoveTicket(ctx context.Context, ticketID string, target domain.Column, comment string) (*domain.Ticket, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireNonEmptyTrimmed("ticket id", ticketID); err != nil {
		return nil, err
	}
	if err := requireMoveTargetColumn(target); err != nil {
		return nil, err
	}
	if err := requireNonEmptyTrimmed("comment", comment); err != nil {
		return nil, err
	}

	st, slug, err := s.hostStoreForTicket(ticketID)
	if err != nil {
		return nil, err
	}
	lp, _, err := s.Cache.Get(ctx, slug)
	if err != nil {
		return nil, err
	}

	if err := s.authorizeActingFor(agent, lp.Project.ID, true); err != nil {
		return nil, err
	}

	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	t, ok := lp.Tickets[ticketID]
	if !ok {
		return nil, fmt.Errorf("%w: ticket %s", domain.ErrNotFound, ticketID)
	}
	if t.Column == domain.ColumnDone {
		return nil, fmt.Errorf("%w: ticket %s is done; reopening is not allowed", domain.ErrFailedPrecondition, ticketID)
	}
	// Ideas stay parked in todo — the only forward path is promotion. You can
	// still comment on them, but they can't walk into the work columns.
	if t.Kind == domain.KindIdea && target != domain.ColumnTodo {
		return nil, fmt.Errorf("%w: ticket %s is an idea and stays in todo; promote it to a work ticket first with promote_idea", domain.ErrFailedPrecondition, ticketID)
	}

	// Dependency enforcement only fires on transitions to in_progress. Other
	// transitions (e.g. todo→testing, in_progress→testing) don't gate on deps.
	commentBody := strings.TrimSpace(comment)
	if target == domain.ColumnInProgress {
		blocked := computeBlockedBy(t.DependsOn, lp.Tickets)
		if len(blocked) > 0 {
			if s.Cfg.EnforceDependencies {
				return nil, fmt.Errorf("%w: ticket %s has unmet dependencies: %v", domain.ErrFailedPrecondition, ticketID, blocked)
			}
			s.Logger.Warn("MoveTicket: unmet deps but enforce_dependencies=false; proceeding",
				"ticket_id", ticketID, "blocked_by", blocked)
			commentBody = fmt.Sprintf("⚠ moved with unmet deps: %v\n\n%s", blocked, commentBody)
		}
	}

	// Resolve the on-disk ticket dir + number for the StageOp paths and the
	// auto-commit caption. This walks the project tree but is cheap at hobby
	// scale and matches the pattern T05 uses elsewhere in this file.
	relTicketDir, ticketNumber, err := s.findTicketDirAndNumber(st, slug, ticketID)
	if err != nil {
		return nil, err
	}

	// Re-read the on-disk record so we don't drop fields the cache doesn't
	// model (forward-compat: another agent's binary may write fields ours
	// doesn't recognize).
	rec := &store.TicketRecord{}
	absYAML := filepath.Join(st.Root, relTicketDir, "ticket.yaml")
	if err := store.ReadYAML(absYAML, rec); err != nil {
		return nil, fmt.Errorf("read ticket: %w", err)
	}
	oldColumn := rec.Column
	now := time.Now()
	rec.Column = target
	rec.UpdatedAt = now
	yamlBytes, err := store.MarshalYAML(rec)
	if err != nil {
		return nil, err
	}

	// system_move comment — same filename convention T06's CreateComment uses.
	commentID := uuid.New()
	createdAt := now.UTC()
	cRec := &store.CommentRecord{
		ID:              commentID.String(),
		TicketID:        ticketID,
		Kind:            domain.CommentKindSystemMove,
		AuthorAgentID:   &agent.ID,
		AuthorForUserID: actingForUserID(agent),
		FromColumn:      &oldColumn,
		ToColumn:        &target,
		CreatedAt:       createdAt,
	}
	shortID := hex.EncodeToString(commentID[:4])
	commentFilename := fmt.Sprintf("%s-%s-%s.md", createdAt.Format(commentTimestampLayout), shortID, string(cRec.Kind))
	commentBodyOut := ensureTrailingNewline(commentBody)
	commentBytes, err := store.EncodeMarkdown(cRec, commentBodyOut)
	if err != nil {
		return nil, fmt.Errorf("encode system_move comment: %w", err)
	}
	relCommentPath := filepath.Join(relTicketDir, "comments", commentFilename)

	// Single StageOp stages BOTH writes; Commit applies them under the
	// per-project flock, so a reader observes either old-state or new-state —
	// never a half-applied move.
	op, err := st.BeginOp()
	if err != nil {
		return nil, err
	}
	defer op.Abort()
	if err := op.Write(filepath.Join(relTicketDir, "ticket.yaml"), yamlBytes); err != nil {
		return nil, err
	}
	if err := op.Write(relCommentPath, commentBytes); err != nil {
		return nil, err
	}
	caption := fmt.Sprintf("move ticket %s/%03d %s→%s", slug, ticketNumber, oldColumn, target)
	if err := op.Commit(ctx, store.LockProject(slug), agent, caption); err != nil {
		return nil, fmt.Errorf("commit move ticket: %w", err)
	}

	// Async embed: system_move comment.
	if mount := s.mountForSlug(slug); mount != nil && mount.Worker != nil {
		commentAbs := filepath.Join(st.Root, relCommentPath)
		stem := strings.TrimSuffix(filepath.Base(commentAbs), ".md")
		mount.Worker.Enqueue(worker.Job{
			Kind:        worker.JobComment,
			SourcePath:  commentAbs,
			SidecarPath: filepath.Join(filepath.Dir(commentAbs), stem+".embedding.json"),
			EntryID:     cRec.ID,
			Owner:       slug,
			Text:        commentBodyOut,
		})
	}

	// Apply mutations to the cached state. Lock is held above.
	t.Column = target
	t.UpdatedAt = rec.UpdatedAt
	domComment := &domain.Comment{
		ID:         cRec.ID,
		TicketID:   cRec.TicketID,
		Kind:       cRec.Kind,
		Body:       commentBodyOut,
		FromColumn: cRec.FromColumn,
		ToColumn:   cRec.ToColumn,
		Author:     hydrateAgentRef(s.AgentStore, agent.ID, agent.Name),
		AuthorFor:  actingForRef(agent),
		CreatedAt:  cRec.CreatedAt,
	}
	lp.Comments[ticketID] = append(lp.Comments[ticketID], domComment)

	s.publish(withActor(eventbus.Event{
		Kind:        eventbus.KindTicketMoved,
		Topics:      ticketTopics(ticketID, lp.Project.ID, t.PhaseID),
		TicketID:    ticketID,
		ProjectID:   lp.Project.ID,
		PhaseID:     derefStr(t.PhaseID),
		FromColumn:  string(oldColumn),
		ToColumn:    string(target),
		CommentID:   cRec.ID,
		CommentKind: string(cRec.Kind),
		ClientID:    ClientIDFrom(ctx),
	}, agent))

	cp := cloneTicket(t)
	cp.BlockedBy = computeBlockedBy(cp.DependsOn, lp.Tickets)
	return cp, nil
}

// CompleteTicket is the only path that can move a ticket into ColumnDone.
// `learnings` is required (≥10 chars after trim) — it's the field future
// agents search via search_learnings, so we keep a gate against thin entries.
// `testing_evidence` and `work_summary` are optional audit-trail fields;
// callers may pass empty strings and the corresponding sections are then
// omitted from completion.md and the system_completion comment.
//
// Three files are staged in a single StageOp: the updated ticket.yaml (with
// CompletedAt, CompletedByAgentID, populated completion strings), a fresh
// completion.md (Learnings always present; the other two sections only when
// supplied), and a system_completion comment whose body inlines the same
// content so ListComments shows it without extra plumbing.
//
// "Done" is sticky — re-completing an already-done ticket returns
// ErrFailedPrecondition (the no-reopen rule from SPEC §Design decisions).
func (s *Service) CompleteTicket(ctx context.Context, ticketID, testingEvidence, workSummary, learnings string) (*domain.Ticket, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireNonEmptyTrimmed("ticket id", ticketID); err != nil {
		return nil, err
	}
	if err := requireMinLen("learnings", learnings, completionMinLen); err != nil {
		return nil, err
	}

	st, slug, err := s.hostStoreForTicket(ticketID)
	if err != nil {
		return nil, err
	}
	lp, _, err := s.Cache.Get(ctx, slug)
	if err != nil {
		return nil, err
	}

	if err := s.authorizeActingFor(agent, lp.Project.ID, true); err != nil {
		return nil, err
	}

	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	t, ok := lp.Tickets[ticketID]
	if !ok {
		return nil, fmt.Errorf("%w: ticket %s", domain.ErrNotFound, ticketID)
	}
	if t.Kind == domain.KindIdea {
		return nil, fmt.Errorf("%w: ticket %s is an idea; ideas can't be completed — promote it to a work ticket first with promote_idea", domain.ErrFailedPrecondition, ticketID)
	}
	if t.Column == domain.ColumnDone {
		return nil, fmt.Errorf("%w: ticket %s is already done", domain.ErrFailedPrecondition, ticketID)
	}

	relTicketDir, ticketNumber, err := s.findTicketDirAndNumber(st, slug, ticketID)
	if err != nil {
		return nil, err
	}

	// Trimmed values land in storage (the loader strip-trims when it parses
	// completion.md sections back, so this keeps the round-trip stable).
	teTrim := strings.TrimSpace(testingEvidence)
	wsTrim := strings.TrimSpace(workSummary)
	lnTrim := strings.TrimSpace(learnings)

	// Re-read on-disk record to preserve any fields the cache doesn't model.
	rec := &store.TicketRecord{}
	absYAML := filepath.Join(st.Root, relTicketDir, "ticket.yaml")
	if err := store.ReadYAML(absYAML, rec); err != nil {
		return nil, fmt.Errorf("read ticket: %w", err)
	}
	now := time.Now()
	rec.Column = domain.ColumnDone
	rec.CompletedAt = &now
	rec.CompletedByAgentID = &agent.ID
	rec.CompletedForUserID = actingForUserID(agent)
	rec.UpdatedAt = now
	yamlBytes, err := store.MarshalYAML(rec)
	if err != nil {
		return nil, err
	}

	// Canonical completion.md. The cache loader's splitCompletionSections
	// matches "## Testing evidence", "## Work summary", and "## Learnings"
	// headings exactly. Optional sections are omitted entirely when empty so
	// the rendered ticket has no dangling blank-bodied headings.
	var mdParts []string
	if teTrim != "" {
		mdParts = append(mdParts, "## Testing evidence\n"+teTrim)
	}
	if wsTrim != "" {
		mdParts = append(mdParts, "## Work summary\n"+wsTrim)
	}
	mdParts = append(mdParts, "## Learnings\n"+lnTrim)
	completionMD := strings.Join(mdParts, "\n\n") + "\n"

	// system_completion comment — body is the formatted multi-section text the
	// SPEC example shows so list_comments surfaces it inline without re-reading
	// completion.md.
	commentID := uuid.New()
	createdAt := now.UTC()
	cRec := &store.CommentRecord{
		ID:              commentID.String(),
		TicketID:        ticketID,
		Kind:            domain.CommentKindSystemCompletion,
		AuthorAgentID:   &agent.ID,
		AuthorForUserID: actingForUserID(agent),
		FromColumn:      nil,
		ToColumn:        nil,
		CreatedAt:       createdAt,
	}
	shortID := hex.EncodeToString(commentID[:4])
	commentFilename := fmt.Sprintf("%s-%s-%s.md", createdAt.Format(commentTimestampLayout), shortID, string(cRec.Kind))
	var commentParts []string
	if teTrim != "" {
		commentParts = append(commentParts, "Testing evidence:\n"+teTrim)
	}
	if wsTrim != "" {
		commentParts = append(commentParts, "Work summary:\n"+wsTrim)
	}
	commentParts = append(commentParts, "Learnings:\n"+lnTrim)
	commentBody := "✅ Ticket completed.\n\n" + strings.Join(commentParts, "\n\n") + "\n"
	commentBytes, err := store.EncodeMarkdown(cRec, commentBody)
	if err != nil {
		return nil, fmt.Errorf("encode system_completion comment: %w", err)
	}

	op, err := st.BeginOp()
	if err != nil {
		return nil, err
	}
	defer op.Abort()
	if err := op.Write(filepath.Join(relTicketDir, "ticket.yaml"), yamlBytes); err != nil {
		return nil, err
	}
	if err := op.Write(filepath.Join(relTicketDir, "completion.md"), []byte(completionMD)); err != nil {
		return nil, err
	}
	if err := op.Write(filepath.Join(relTicketDir, "comments", commentFilename), commentBytes); err != nil {
		return nil, err
	}
	caption := fmt.Sprintf("complete ticket %s/%03d", slug, ticketNumber)
	if err := op.Commit(ctx, store.LockProject(slug), agent, caption); err != nil {
		return nil, fmt.Errorf("commit complete ticket: %w", err)
	}

	// Async embed: learnings → mount's LearningsIdx, plus the
	// system_completion comment → mount's CommentsIdx.
	if mount := s.mountForSlug(slug); mount != nil && mount.Worker != nil {
		ticketDirAbs := filepath.Join(st.Root, relTicketDir)
		mount.Worker.Enqueue(worker.Job{
			Kind:        worker.JobTicketLearnings,
			SourcePath:  filepath.Join(ticketDirAbs, "completion.md"),
			SidecarPath: filepath.Join(ticketDirAbs, "learnings.embedding.json"),
			EntryID:     rec.ID,
			Owner:       slug,
			Text:        lnTrim,
		})
		commentAbs := filepath.Join(ticketDirAbs, "comments", commentFilename)
		stem := strings.TrimSuffix(filepath.Base(commentAbs), ".md")
		mount.Worker.Enqueue(worker.Job{
			Kind:        worker.JobComment,
			SourcePath:  commentAbs,
			SidecarPath: filepath.Join(filepath.Dir(commentAbs), stem+".embedding.json"),
			EntryID:     cRec.ID,
			Owner:       slug,
			Text:        commentBody,
		})
	}

	// Apply mutations to the cached state. Lock is held above.
	t.Column = domain.ColumnDone
	t.TestingEvidence = strPtr(teTrim)
	t.WorkSummary = strPtr(wsTrim)
	t.Learnings = strPtr(lnTrim)
	t.CompletedAt = &now
	t.CompletedBy = hydrateAgentRef(s.AgentStore, agent.ID, agent.Name)
	t.CompletedFor = actingForRef(agent)
	t.UpdatedAt = rec.UpdatedAt
	domComment := &domain.Comment{
		ID:         cRec.ID,
		TicketID:   cRec.TicketID,
		Kind:       cRec.Kind,
		Body:       commentBody,
		FromColumn: cRec.FromColumn,
		ToColumn:   cRec.ToColumn,
		Author:     hydrateAgentRef(s.AgentStore, agent.ID, agent.Name),
		AuthorFor:  actingForRef(agent),
		CreatedAt:  cRec.CreatedAt,
	}
	lp.Comments[ticketID] = append(lp.Comments[ticketID], domComment)

	s.publish(withActor(eventbus.Event{
		Kind:        eventbus.KindTicketCompleted,
		Topics:      ticketTopics(ticketID, lp.Project.ID, t.PhaseID),
		TicketID:    ticketID,
		ProjectID:   lp.Project.ID,
		PhaseID:     derefStr(t.PhaseID),
		ToColumn:    string(domain.ColumnDone),
		CommentID:   cRec.ID,
		CommentKind: string(cRec.Kind),
	}, agent))

	cp := cloneTicket(t)
	cp.BlockedBy = computeBlockedBy(cp.DependsOn, lp.Tickets)
	return cp, nil
}

// DeleteTicket hard-removes a non-`done` ticket and everything under its
// directory (body, comments, embedding sidecars). Refuses on `done` so the
// SPEC's "completion is sacred" rule survives.
//
// Cascade behaviour: any other ticket in the same project whose DependsOn or
// ParallelizableWith slice contains the doomed id has those entries stripped
// and its `ticket.yaml` rewritten under the same StageOp. The doomed ticket's
// RemovePath plus the cascade rewrites all commit together under the
// per-project flock, so callers never observe dangling refs.
//
// In-memory cache + the resident vec indexes (TicketsIdx, LearningsIdx,
// CommentsIdx) are pruned post-commit so the now-deleted ticket and its
// comments stop appearing in search; affected dependents have their cached
// slices mutated in place and BlockedBy recomputed.
func (s *Service) DeleteTicket(ctx context.Context, id string) error {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return err
	}
	if err := requireNonEmptyTrimmed("ticket id", id); err != nil {
		return err
	}

	_, hostSlug, err := s.hostStoreForTicket(id)
	if err != nil {
		return err
	}
	st, err := s.ResolveProjectStore(ctx, hostSlug)
	if err != nil {
		return err
	}
	lp, _, err := s.Cache.Get(ctx, hostSlug)
	if err != nil {
		return err
	}

	if err := s.authorizeActingFor(agent, lp.Project.ID, true); err != nil {
		return err
	}

	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	t, ok := lp.Tickets[id]
	if !ok {
		return fmt.Errorf("%w: ticket %s", domain.ErrNotFound, id)
	}
	if t.Column == domain.ColumnDone {
		return fmt.Errorf("%w: ticket %s is done; completed tickets are frozen and cannot be deleted (SPEC: no reopen, no delete after complete)", domain.ErrFailedPrecondition, id)
	}

	// Build the cascade list: every other ticket whose DependsOn or
	// ParallelizableWith carries the doomed id. We re-read each yaml from
	// disk rather than serialising the cache so we preserve fields the
	// cache doesn't model (CompletedByAgentID, etc.) — same pattern as
	// UpdateTicket.
	type cascadeUpdate struct {
		ticketID string
		relDir   string
		rec      *store.TicketRecord
	}
	var cascade []cascadeUpdate
	for _, other := range lp.Tickets {
		if other.ID == id {
			continue
		}
		hasDep := containsID(other.DependsOn, id)
		hasPar := containsID(other.ParallelizableWith, id)
		if !hasDep && !hasPar {
			continue
		}
		relDir, absDir, err := s.findTicketDir(st, hostSlug, other.ID)
		if err != nil {
			return fmt.Errorf("locate dependent ticket %s: %w", other.ID, err)
		}
		rec := &store.TicketRecord{}
		if err := store.ReadYAML(filepath.Join(absDir, "ticket.yaml"), rec); err != nil {
			return fmt.Errorf("read dependent ticket %s: %w", other.ID, err)
		}
		rec.DependsOn = removeID(rec.DependsOn, id)
		rec.ParallelizableWith = removeID(rec.ParallelizableWith, id)
		rec.UpdatedAt = time.Now()
		cascade = append(cascade, cascadeUpdate{ticketID: other.ID, relDir: relDir, rec: rec})
	}

	ticketDirRel, _, err := s.findTicketDir(st, hostSlug, id)
	if err != nil {
		return err
	}

	// Drain pending embed jobs on the mount's worker so it doesn't write a
	// sidecar into the ticket dir we're about to RemovePath. Mirrors
	// DeleteProject / DeletePhase.
	if mount := s.mountForSlug(hostSlug); mount != nil && mount.Worker != nil {
		mount.Worker.Flush(ctx)
	}

	op, err := st.BeginOp()
	if err != nil {
		return err
	}
	defer op.Abort()
	for _, cu := range cascade {
		yamlBytes, err := store.MarshalYAML(cu.rec)
		if err != nil {
			return fmt.Errorf("marshal dependent %s: %w", cu.ticketID, err)
		}
		if err := op.Write(filepath.Join(cu.relDir, "ticket.yaml"), yamlBytes); err != nil {
			return err
		}
	}
	if err := op.RemovePath(ticketDirRel); err != nil {
		return err
	}
	caption := fmt.Sprintf("delete ticket %s/%s", hostSlug, id)
	if len(cascade) > 0 {
		caption = fmt.Sprintf("%s (cleared %d dependent ref(s))", caption, len(cascade))
	}
	if err := op.Commit(ctx, store.LockProject(hostSlug), agent, caption); err != nil {
		return fmt.Errorf("commit delete ticket: %w", err)
	}

	// Apply the cascade to the in-memory cache so subsequent reads see
	// consistent state without waiting for fsnotify.
	for _, cu := range cascade {
		other, ok := lp.Tickets[cu.ticketID]
		if !ok {
			continue
		}
		other.DependsOn = removeID(other.DependsOn, id)
		other.ParallelizableWith = removeID(other.ParallelizableWith, id)
		other.UpdatedAt = cu.rec.UpdatedAt
	}

	// Prune cache + resident indexes. Comments slice may be nil for tickets
	// that never received any; range over nil is fine.
	s.mountsMu.Lock()
	mount := s.projectMounts[hostSlug]
	s.mountsMu.Unlock()
	feedbackKeys := []domain.EntryKey{
		domain.TicketEntryKey(id),
		domain.LearningEntryKey(id),
	}
	for _, c := range lp.Comments[id] {
		if mount != nil && mount.CommentsIdx != nil {
			mount.CommentsIdx.Delete(c.ID)
		}
		if s.defaultIndexes.Comments != nil {
			s.defaultIndexes.Comments.Delete(c.ID)
		}
		feedbackKeys = append(feedbackKeys, domain.CommentEntryKey(c.ID))
	}
	delete(lp.Tickets, id)
	delete(lp.Comments, id)
	if mount != nil && mount.TicketsIdx != nil {
		mount.TicketsIdx.Delete(id)
	}
	if s.defaultIndexes.Tickets != nil {
		s.defaultIndexes.Tickets.Delete(id)
	}
	if mount != nil && mount.LearningsIdx != nil {
		// Defensive: a non-done ticket has no learnings entry, but a future
		// edit might land here under a state we didn't anticipate.
		mount.LearningsIdx.Delete(id)
	}
	if s.defaultIndexes.Learnings != nil {
		s.defaultIndexes.Learnings.Delete(id)
	}
	// Drop the doomed ticket's feedback entries (ticket + learning + every
	// comment) in one write. Failure is logged-but-non-fatal: a leftover
	// feedback row pointing at a deleted id is harmless beyond a tiny bit of
	// disk noise; the lookup just misses on next access.
	if mount != nil && mount.Feedback != nil {
		if err := mount.Feedback.DeleteMany(ctx, feedbackKeys); err != nil && s.Logger != nil {
			s.Logger.Warn("svc: delete ticket feedback cascade failed",
				"slug", hostSlug, "ticket_id", id, "err", err)
		}
	}
	return nil
}

// strPtr returns a pointer to s. Used for setting *string fields on
// domain.Ticket without an extra named local.
func strPtr(s string) *string { return &s }

// resolvePhase looks up a phase by id or slug in the loaded project. Returns
// (phase, true) on hit.
func resolvePhase(lp *cache.LoadedProject, idOrSlug string) (*domain.Phase, bool) {
	if ph, ok := lp.Phases[idOrSlug]; ok {
		return ph, true
	}
	if ph, ok := lp.PhasesBySlug[idOrSlug]; ok {
		return ph, true
	}
	return nil, false
}

// nextTicketNumber returns max(existing)+1 for the project's tickets,
// scanning every yaml under both phase-less and phased dirs. Numbering is
// project-global per SPEC.
func (s *Service) nextTicketNumber(st *store.Store, slug string) (int, error) {
	max := 0
	if err := st.WalkTickets(slug, func(_, _ string, tr *store.TicketRecord) error {
		if tr.Number > max {
			max = tr.Number
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("walk tickets for numbering: %w", err)
	}
	return max + 1, nil
}

// uniqueTicketDirName returns a `<NNN>-<slug>` directory name unique on
// disk within the project (across both phase-less and phased tickets).
// Collisions on the same NNN-slug pair (rare with the number prefix) get
// a `-2`, `-3`, … suffix.
func (s *Service) uniqueTicketDirName(st *store.Store, slug string, number int, title string) (string, error) {
	taken := map[string]bool{}
	if err := st.WalkTickets(slug, func(ticketDir, _ string, _ *store.TicketRecord) error {
		taken[filepath.Base(ticketDir)] = true
		return nil
	}); err != nil {
		return "", fmt.Errorf("walk tickets for collision check: %w", err)
	}
	base := fmt.Sprintf("%03d-%s", number, domain.MakeSlug(title))
	if !taken[base] {
		return base, nil
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !taken[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not pick a unique ticket dir name under %q", base)
}

// containsID reports whether ids contains target.
func containsID(ids []string, target string) bool {
	for _, x := range ids {
		if x == target {
			return true
		}
	}
	return false
}

// removeID returns a copy of ids with every occurrence of target dropped. A
// nil input yields a nil output; an absent target yields the same slice
// (modulo nilness) so callers can detect "nothing changed" via len comparison.
func removeID(ids []string, target string) []string {
	if len(ids) == 0 {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, x := range ids {
		if x == target {
			continue
		}
		out = append(out, x)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// computeBlockedBy returns the subset of depIDs whose tickets are not yet
// `done`. Missing dep ids are surfaced as still-blocked (matches the cache
// loader's semantics).
func computeBlockedBy(depIDs []string, tickets map[string]*domain.Ticket) []string {
	if len(depIDs) == 0 {
		return nil
	}
	out := make([]string, 0)
	for _, dep := range depIDs {
		dt, ok := tickets[dep]
		if !ok || dt.Column != domain.ColumnDone {
			out = append(out, dep)
		}
	}
	return out
}

func validateTicketRefs(lp *cache.LoadedProject, ticketID, field string, refs []string) error {
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if ref == ticketID {
			return fmt.Errorf("%w: %s cannot reference the ticket itself", domain.ErrInvalidArgument, field)
		}
		rt, ok := lp.Tickets[ref]
		if !ok {
			return fmt.Errorf("%w: %s ticket %q not in project", domain.ErrInvalidArgument, field, ref)
		}
		if rt.Kind == domain.KindIdea {
			return fmt.Errorf("%w: %s ticket %q is an idea; ideas can't gate work — promote it first with promote_idea", domain.ErrInvalidArgument, field, ref)
		}
	}
	return nil
}

func compactTicketRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref = strings.TrimSpace(ref); ref != "" {
			out = append(out, ref)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func dependencyUpdateCreatesCycle(lp *cache.LoadedProject, ticketID string, newDependsOn []string) bool {
	visited := map[string]bool{}
	var reachesTarget func(string) bool
	reachesTarget = func(id string) bool {
		if id == ticketID {
			return true
		}
		if visited[id] {
			return false
		}
		visited[id] = true
		t, ok := lp.Tickets[id]
		if !ok {
			return false
		}
		for _, dep := range t.DependsOn {
			if reachesTarget(dep) {
				return true
			}
		}
		return false
	}
	for _, dep := range compactTicketRefs(newDependsOn) {
		if reachesTarget(dep) {
			return true
		}
	}
	return false
}

// findTicketDir returns the relative-to-Store.Root and absolute path of the
// ticket directory matching the given ticket id within the named project.
// Used by UpdateTicket so it can write back to the existing directory (which
// might be phased or phase-less). The supplied store must host `slug` —
// callers obtain it via hostStoreForTicket or ResolveProjectStore.
func (s *Service) findTicketDir(st *store.Store, slug, id string) (string, string, error) {
	var relDir, absDir string
	if err := st.WalkTickets(slug, func(ticketDir, phaseDirName string, tr *store.TicketRecord) error {
		if tr.ID != id {
			return nil
		}
		base := filepath.Base(ticketDir)
		if phaseDirName == "" {
			relDir = filepath.Join("tickets", base)
		} else {
			relDir = filepath.Join("phases", phaseDirName, "tickets", base)
		}
		absDir = ticketDir
		return nil
	}); err != nil {
		return "", "", fmt.Errorf("walk tickets: %w", err)
	}
	if relDir == "" {
		return "", "", fmt.Errorf("%w: ticket %s in project %s", domain.ErrNotFound, id, slug)
	}
	return relDir, absDir, nil
}

// phaseFilterMatches applies the SPEC's phase filter sentinel rules to a
// ticket. nil = any; *"-" = phase-less only; *"<id|slug>" = matches the
// ticket's PhaseID (or phase Slug, resolved via lp.PhasesBySlug).
func phaseFilterMatches(t *domain.Ticket, filter *string, lp *cache.LoadedProject) bool {
	if filter == nil {
		return true
	}
	if *filter == "-" {
		return t.PhaseID == nil
	}
	if t.PhaseID == nil {
		return false
	}
	if *t.PhaseID == *filter {
		return true
	}
	// Filter might be a slug.
	if ph, ok := lp.PhasesBySlug[*filter]; ok && ph.ID == *t.PhaseID {
		return true
	}
	return false
}

// ticketLess implements the SPEC's wave-then-creation ordering: Wave
// ascending with wave 0 (unassigned) sorted LAST; tickets in the same wave
// ordered by CreatedAt ascending; final tie-break by ID for determinism.
func ticketLess(a, b *domain.Ticket) bool {
	aw, bw := a.Wave, b.Wave
	// Wave 0 sentinel: sort last. Translate so wave 0 maps to MaxInt.
	if aw == 0 {
		aw = int(^uint(0) >> 1)
	}
	if bw == 0 {
		bw = int(^uint(0) >> 1)
	}
	if aw != bw {
		return aw < bw
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

// encodeCursor produces a `<rfc3339>|<id>` cursor base64-encoded. RFC3339Nano
// preserves sub-second precision so very fast successive Creates don't
// collide on the timestamp portion.
func encodeCursor(ts time.Time, id string) string {
	raw := ts.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor reverses encodeCursor, returning ErrInvalidArgument on any
// kind of malformed input — empty string is the caller's responsibility to
// not pass.
func decodeCursor(s string) (time.Time, string, error) {
	data, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: cursor not base64", domain.ErrInvalidArgument)
	}
	idx := strings.IndexByte(string(data), '|')
	if idx <= 0 || idx >= len(data)-1 {
		return time.Time{}, "", fmt.Errorf("%w: cursor missing separator", domain.ErrInvalidArgument)
	}
	tsStr := string(data[:idx])
	id := string(data[idx+1:])
	ts, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: cursor timestamp unparseable", domain.ErrInvalidArgument)
	}
	if id == "" {
		return time.Time{}, "", fmt.Errorf("%w: cursor missing id", domain.ErrInvalidArgument)
	}
	return ts, id, nil
}

// cloneTicket returns a deep copy of a ticket so callers can't mutate the
// cache.
func cloneTicket(t *domain.Ticket) *domain.Ticket {
	cp := *t
	cp.DependsOn = append([]string(nil), t.DependsOn...)
	cp.ParallelizableWith = append([]string(nil), t.ParallelizableWith...)
	cp.BlockedBy = append([]string(nil), t.BlockedBy...)
	return &cp
}
