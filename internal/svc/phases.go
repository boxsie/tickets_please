package svc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/cache"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/worker"
)

// CreatePhase stages phase.yaml + summary.md under the project's flock and
// inserts the freshly-hydrated *domain.Phase into the loaded project's cache
// entry so subsequent reads see it without waiting for fsnotify.
func (s *Service) CreatePhase(ctx context.Context, projectIDOrSlug, name, description, summary string) (*domain.Phase, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}

	name = strings.TrimSpace(name)
	if err := requireNonEmptyTrimmed("phase name", name); err != nil {
		return nil, err
	}
	if err := requireSummary("phase summary", summary); err != nil {
		return nil, err
	}

	lp, _, err := s.Cache.Get(ctx, projectIDOrSlug)
	if err != nil {
		return nil, err
	}

	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	phaseSlug := domain.MakeSlug(name)
	if _, dup := lp.PhasesBySlug[phaseSlug]; dup {
		return nil, fmt.Errorf("%w: phase slug %q already exists in project %s", domain.ErrAlreadyExists, phaseSlug, lp.Project.Slug)
	}

	// Compute the next phase number from the cached map. Holding the project
	// write lock keeps two concurrent CreatePhase calls from picking the same
	// number; the per-project flock during commit guards against cross-process
	// races.
	maxNumber := 0
	for _, ph := range lp.Phases {
		if ph.Number > maxNumber {
			maxNumber = ph.Number
		}
	}
	number := maxNumber + 1

	phaseDirName := fmt.Sprintf("%03d-%s", number, phaseSlug)
	phaseDirRel := filepath.Join("projects", lp.Project.Slug, "phases", phaseDirName)

	now := time.Now()
	rec := &store.PhaseRecord{
		ID:               uuid.NewString(),
		ProjectID:        lp.Project.ID,
		Slug:             phaseSlug,
		Number:           number,
		Name:             name,
		Description:      strings.TrimSpace(description),
		CreatedByAgentID: &agent.ID,
		CreatedAt:        now,
		UpdatedAt:        now,
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
	if err := op.Write(filepath.Join(phaseDirRel, "phase.yaml"), yamlBytes); err != nil {
		return nil, err
	}
	if err := op.Write(filepath.Join(phaseDirRel, "summary.md"), []byte(ensureTrailingNewline(summary))); err != nil {
		return nil, err
	}
	caption := fmt.Sprintf("create phase %s/%s", lp.Project.Slug, phaseSlug)
	if err := op.Commit(ctx, store.LockProject(lp.Project.Slug), agent, caption); err != nil {
		return nil, fmt.Errorf("commit create phase: %w", err)
	}

	// Async embed: phase summary → resident SummaryIdx (phase summaries
	// share the same kind as project summaries — see SPEC §Vector search).
	if s.Worker != nil {
		phaseDirAbs := filepath.Join(s.Store.Root, phaseDirRel)
		s.Worker.Enqueue(worker.Job{
			Kind:        worker.JobProjectSummary,
			SourcePath:  filepath.Join(phaseDirAbs, "summary.md"),
			SidecarPath: filepath.Join(phaseDirAbs, "summary.embedding.json"),
			EntryID:     rec.ID,
			Owner:       lp.Project.Slug,
			Text:        summary,
		})
	}

	ph := &domain.Phase{
		ID:          rec.ID,
		ProjectID:   rec.ProjectID,
		Slug:        rec.Slug,
		Number:      rec.Number,
		Name:        rec.Name,
		Description: rec.Description,
		Summary:     summary,
		CreatedBy:   &domain.AgentRef{ID: agent.ID, Name: agent.Name},
		CreatedAt:   rec.CreatedAt,
		UpdatedAt:   rec.UpdatedAt,
	}
	lp.Phases[ph.ID] = ph
	lp.PhasesBySlug[ph.Slug] = ph

	cp := *ph
	return &cp, nil
}

// GetPhase returns the hydrated phase including computed ticket counts.
// Read-only — no requireSession.
func (s *Service) GetPhase(ctx context.Context, projectIDOrSlug, phaseIDOrSlug string) (*domain.Phase, error) {
	if strings.TrimSpace(phaseIDOrSlug) == "" {
		return nil, fmt.Errorf("%w: phase id or slug required", domain.ErrInvalidArgument)
	}
	lp, _, err := s.Cache.Get(ctx, projectIDOrSlug)
	if err != nil {
		return nil, err
	}
	lp.Lock.RLock()
	defer lp.Lock.RUnlock()

	ph, ok := resolvePhase(lp, phaseIDOrSlug)
	if !ok {
		return nil, fmt.Errorf("%w: phase %q in project %s", domain.ErrNotFound, phaseIDOrSlug, lp.Project.Slug)
	}
	out := hydratePhaseWithSummary(s.Store, lp, ph)
	return out, nil
}

// ListPhases returns every phase in the project ordered by Number ascending,
// each carrying computed ticket counts. Read-only.
func (s *Service) ListPhases(ctx context.Context, projectIDOrSlug string) ([]*domain.Phase, error) {
	lp, _, err := s.Cache.Get(ctx, projectIDOrSlug)
	if err != nil {
		return nil, err
	}
	lp.Lock.RLock()
	defer lp.Lock.RUnlock()

	out := make([]*domain.Phase, 0, len(lp.Phases))
	for _, ph := range lp.Phases {
		out = append(out, hydratePhaseWithSummary(s.Store, lp, ph))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// UpdatePhase mutates name/description/summary on a phase. Same shape as
// UpdateProject — re-reads the on-disk record so we don't drop unmodelled
// fields, applies the StageOp under the project flock, and updates the
// cached phase in place.
func (s *Service) UpdatePhase(ctx context.Context, projectIDOrSlug, phaseIDOrSlug string, in domain.UpdatePhaseInput) (*domain.Phase, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(phaseIDOrSlug) == "" {
		return nil, fmt.Errorf("%w: phase id or slug required", domain.ErrInvalidArgument)
	}

	lp, _, err := s.Cache.Get(ctx, projectIDOrSlug)
	if err != nil {
		return nil, err
	}
	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	ph, ok := resolvePhase(lp, phaseIDOrSlug)
	if !ok {
		return nil, fmt.Errorf("%w: phase %q in project %s", domain.ErrNotFound, phaseIDOrSlug, lp.Project.Slug)
	}

	var newSummary *string
	if in.Summary != nil {
		if err := requireSummary("phase summary", *in.Summary); err != nil {
			return nil, err
		}
		newSummary = in.Summary
	}

	phaseDirName := fmt.Sprintf("%03d-%s", ph.Number, ph.Slug)
	phaseDirRel := filepath.Join("projects", lp.Project.Slug, "phases", phaseDirName)

	rec := &store.PhaseRecord{}
	if err := store.ReadYAML(filepath.Join(s.Store.Root, phaseDirRel, "phase.yaml"), rec); err != nil {
		return nil, fmt.Errorf("read phase: %w", err)
	}
	if in.Name != nil {
		rec.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		rec.Description = strings.TrimSpace(*in.Description)
	}
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
	if err := op.Write(filepath.Join(phaseDirRel, "phase.yaml"), yamlBytes); err != nil {
		return nil, err
	}
	if newSummary != nil {
		if err := op.Write(filepath.Join(phaseDirRel, "summary.md"), []byte(ensureTrailingNewline(*newSummary))); err != nil {
			return nil, err
		}
	}
	caption := fmt.Sprintf("update phase %s/%s", lp.Project.Slug, ph.Slug)
	if err := op.Commit(ctx, store.LockProject(lp.Project.Slug), agent, caption); err != nil {
		return nil, fmt.Errorf("commit update phase: %w", err)
	}

	// Mutate cached phase in place. Lock is held above.
	ph.Name = rec.Name
	ph.Description = rec.Description
	ph.UpdatedAt = rec.UpdatedAt
	if newSummary != nil {
		ph.Summary = *newSummary
		if s.Worker != nil {
			phaseDirAbs := filepath.Join(s.Store.Root, phaseDirRel)
			s.Worker.Enqueue(worker.Job{
				Kind:        worker.JobProjectSummary,
				SourcePath:  filepath.Join(phaseDirAbs, "summary.md"),
				SidecarPath: filepath.Join(phaseDirAbs, "summary.embedding.json"),
				EntryID:     rec.ID,
				Owner:       lp.Project.Slug,
				Text:        *newSummary,
			})
		}
	}

	out := hydratePhaseWithSummary(s.Store, lp, ph)
	return out, nil
}

// DeletePhase removes a phase from disk and cache. Refuses if any ticket is
// still assigned to it — agents must reassign tickets first.
func (s *Service) DeletePhase(ctx context.Context, projectIDOrSlug, phaseIDOrSlug string) error {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(phaseIDOrSlug) == "" {
		return fmt.Errorf("%w: phase id or slug required", domain.ErrInvalidArgument)
	}

	lp, _, err := s.Cache.Get(ctx, projectIDOrSlug)
	if err != nil {
		return err
	}
	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	ph, ok := resolvePhase(lp, phaseIDOrSlug)
	if !ok {
		return fmt.Errorf("%w: phase %q in project %s", domain.ErrNotFound, phaseIDOrSlug, lp.Project.Slug)
	}

	// Block delete if any ticket is still assigned to this phase. SPEC
	// requires a clean phase — agents must reassign tickets first.
	assigned := 0
	for _, t := range lp.Tickets {
		if t.PhaseID != nil && *t.PhaseID == ph.ID {
			assigned++
		}
	}
	if assigned > 0 {
		return fmt.Errorf("%w: phase %s has %d ticket(s) assigned; reassign them first", domain.ErrFailedPrecondition, ph.Slug, assigned)
	}

	phaseDirName := fmt.Sprintf("%03d-%s", ph.Number, ph.Slug)
	phaseDirRel := filepath.Join("projects", lp.Project.Slug, "phases", phaseDirName)

	// Drain pending embed jobs so the worker doesn't recreate the phase
	// dir with a freshly-written sidecar after the upcoming RemovePath.
	if s.Worker != nil {
		s.Worker.Flush(ctx)
	}

	op, err := s.Store.BeginOp()
	if err != nil {
		return err
	}
	defer op.Abort()
	if err := op.RemovePath(phaseDirRel); err != nil {
		return err
	}
	caption := fmt.Sprintf("delete phase %s/%s", lp.Project.Slug, ph.Slug)
	if err := op.Commit(ctx, store.LockProject(lp.Project.Slug), agent, caption); err != nil {
		return fmt.Errorf("commit delete phase: %w", err)
	}

	delete(lp.Phases, ph.ID)
	delete(lp.PhasesBySlug, ph.Slug)
	return nil
}

// hydratePhaseWithSummary returns a fresh *domain.Phase combining the cached
// metadata, computed ticket counts, and the on-disk summary.md (loaded
// lazily — the cache loader doesn't currently fill Summary). Falls back to
// whatever the cached phase already carries if the file read fails.
func hydratePhaseWithSummary(st *store.Store, lp *cache.LoadedProject, ph *domain.Phase) *domain.Phase {
	cp := *ph
	if cp.Summary == "" {
		phaseDirName := fmt.Sprintf("%03d-%s", ph.Number, ph.Slug)
		path := filepath.Join(st.Root, "projects", lp.Project.Slug, "phases", phaseDirName, "summary.md")
		if data, err := readMarkdownIfExists(path); err == nil {
			cp.Summary = data
		}
	}
	total := 0
	active := 0
	for _, t := range lp.Tickets {
		if t.PhaseID == nil || *t.PhaseID != ph.ID {
			continue
		}
		total++
		if t.Column != domain.ColumnDone {
			active++
		}
	}
	cp.TicketCount = total
	cp.ActiveTicketCount = active
	return &cp
}

// readMarkdownIfExists is a friendlier os.ReadFile that returns ("", nil)
// when the file is absent. Used for sidecar summary.md reads where missing
// is a soft case.
func readMarkdownIfExists(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}
