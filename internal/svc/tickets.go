package svc

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/cache"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
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

	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: title required", domain.ErrInvalidArgument)
	}
	if in.Wave < 0 {
		return nil, fmt.Errorf("%w: wave must be >= 0", domain.ErrInvalidArgument)
	}

	lp, _, err := s.Cache.Get(ctx, in.ProjectIDOrSlug)
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
		if _, ok := lp.Tickets[dep]; !ok {
			return nil, fmt.Errorf("%w: depends_on ticket %q not in project", domain.ErrInvalidArgument, dep)
		}
	}
	for _, par := range in.ParallelizableWith {
		if _, ok := lp.Tickets[par]; !ok {
			return nil, fmt.Errorf("%w: parallelizable_with ticket %q not in project", domain.ErrInvalidArgument, par)
		}
	}

	// Project-global ticket numbering. We scan disk because the cache
	// hydrates tickets without their on-disk Number — and deletions can
	// leave gaps, so max+1 is the only safe answer.
	number, err := s.nextTicketNumber(lp.Project.Slug)
	if err != nil {
		return nil, err
	}

	dirName, err := s.uniqueTicketDirName(lp.Project.Slug, number, title)
	if err != nil {
		return nil, err
	}

	// Pick the on-disk path: phased vs phase-less.
	var ticketDirRel string
	if phaseDirName != "" {
		ticketDirRel = filepath.Join("projects", lp.Project.Slug, "phases", phaseDirName, "tickets", dirName)
	} else {
		ticketDirRel = filepath.Join("projects", lp.Project.Slug, "tickets", dirName)
	}

	now := time.Now()
	rec := &store.TicketRecord{
		ID:                 uuid.NewString(),
		ProjectID:          lp.Project.ID,
		Number:             number,
		Title:              title,
		Column:             domain.ColumnTodo,
		PhaseID:            phaseIDPtr,
		Wave:               in.Wave,
		DependsOn:          append([]string(nil), in.DependsOn...),
		ParallelizableWith: append([]string(nil), in.ParallelizableWith...),
		CreatedByAgentID:   &agent.ID,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	yamlBytes, err := store.MarshalYAML(rec)
	if err != nil {
		return nil, err
	}

	op, err := s.Store.BeginOp()
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

	// T10: enqueue embed job here

	t := &domain.Ticket{
		ID:                 rec.ID,
		ProjectID:          rec.ProjectID,
		Title:              rec.Title,
		Body:               in.Body,
		Column:             rec.Column,
		PhaseID:            rec.PhaseID,
		Wave:               rec.Wave,
		DependsOn:          append([]string(nil), rec.DependsOn...),
		ParallelizableWith: append([]string(nil), rec.ParallelizableWith...),
		CreatedBy:          &domain.AgentRef{ID: agent.ID, Name: agent.Name},
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
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: ticket id required", domain.ErrInvalidArgument)
	}

	hostSlug, err := s.resolveTicketProject(id)
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

// UpdateTicket mutates title / body / wave on an existing ticket. Column,
// phase, and dep edges are owned by other methods (T07/T16) and rejected
// here-by-omission since UpdateTicketInput doesn't carry those fields.
func (s *Service) UpdateTicket(ctx context.Context, id string, in domain.UpdateTicketInput) (*domain.Ticket, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: ticket id required", domain.ErrInvalidArgument)
	}
	if in.Wave != nil && *in.Wave < 0 {
		return nil, fmt.Errorf("%w: wave must be >= 0", domain.ErrInvalidArgument)
	}

	// Find the host project. Reuse GetTicket's resolution machinery — but
	// return ErrNotFound on miss without lazy-loading every project.
	hostSlug, err := s.resolveTicketProject(id)
	if err != nil {
		return nil, err
	}
	lp, _, err := s.Cache.Get(ctx, hostSlug)
	if err != nil {
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
	if in.Title != nil {
		nt := strings.TrimSpace(*in.Title)
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

	// Re-read the on-disk record so we don't drop fields the cache doesn't
	// model (e.g. CompletedByAgentID after a T07 lands and then this T05
	// path runs concurrently).
	ticketDirRel, ticketDirAbs, err := s.findTicketDir(lp.Project.Slug, id)
	if err != nil {
		return nil, err
	}
	rec := &store.TicketRecord{}
	if err := store.ReadYAML(filepath.Join(ticketDirAbs, "ticket.yaml"), rec); err != nil {
		return nil, fmt.Errorf("read ticket: %w", err)
	}
	rec.Title = newTitle
	rec.Wave = newWave
	rec.UpdatedAt = time.Now()

	yamlBytes, err := store.MarshalYAML(rec)
	if err != nil {
		return nil, err
	}

	op, err := s.Store.BeginOp()
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
		// T10: enqueue embed job here
	}

	// Apply mutations to the cached ticket in place. Lock is held.
	t.Title = newTitle
	if bodyChanged {
		t.Body = newBody
	}
	t.Wave = newWave
	t.UpdatedAt = rec.UpdatedAt

	cp := cloneTicket(t)
	cp.BlockedBy = computeBlockedBy(cp.DependsOn, lp.Tickets)
	return cp, nil
}

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
func (s *Service) nextTicketNumber(slug string) (int, error) {
	max := 0
	if err := s.Store.WalkTickets(slug, func(_, _ string, tr *store.TicketRecord) error {
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
func (s *Service) uniqueTicketDirName(slug string, number int, title string) (string, error) {
	taken := map[string]bool{}
	if err := s.Store.WalkTickets(slug, func(ticketDir, _ string, _ *store.TicketRecord) error {
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

// resolveTicketProject finds which project hosts the given ticket id by
// walking disk. Returns ErrNotFound if no project does.
func (s *Service) resolveTicketProject(id string) (string, error) {
	var hostSlug string
	if err := s.Store.WalkProjects(func(slug string, _ *store.ProjectRecord) error {
		if hostSlug != "" {
			return nil
		}
		return s.Store.WalkTickets(slug, func(_, _ string, tr *store.TicketRecord) error {
			if tr.ID == id {
				hostSlug = slug
			}
			return nil
		})
	}); err != nil {
		return "", fmt.Errorf("walk projects: %w", err)
	}
	if hostSlug == "" {
		return "", fmt.Errorf("%w: ticket %s", domain.ErrNotFound, id)
	}
	return hostSlug, nil
}

// findTicketDir returns the relative-to-Store.Root and absolute path of the
// ticket directory matching the given ticket id within the named project.
// Used by UpdateTicket so it can write back to the existing directory (which
// might be phased or phase-less).
func (s *Service) findTicketDir(slug, id string) (string, string, error) {
	var relDir, absDir string
	if err := s.Store.WalkTickets(slug, func(ticketDir, phaseDirName string, tr *store.TicketRecord) error {
		if tr.ID != id {
			return nil
		}
		base := filepath.Base(ticketDir)
		if phaseDirName == "" {
			relDir = filepath.Join("projects", slug, "tickets", base)
		} else {
			relDir = filepath.Join("projects", slug, "phases", phaseDirName, "tickets", base)
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

