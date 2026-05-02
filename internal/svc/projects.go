package svc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/cache"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/worker"
)

// projectSlugRE is the server-side validation for project slugs. SPEC §
// Project loading: lowercase letters, digits, dashes, underscores; must
// start and end with [a-z0-9]; 2-64 chars total.
var projectSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}[a-z0-9]$`)

// minProjectSummaryLen is the SPEC-mandated minimum length of project
// summary text (after trim). Mirrored on phase summaries — same load-bearing
// context-doc role per SPEC §Project summary.
const minProjectSummaryLen = 200

// LoadProjectResult is what Service.LoadProject returns. Handle is purely
// diagnostic: subsequent calls just pass slug-or-id, and the cache key is
// always the slug. ExpiresAt = LastAccessAt + idle TTL.
type LoadProjectResult struct {
	Project           *domain.Project
	Handle            string
	ExpiresAt         time.Time
	TicketCount       int
	ActiveTicketCount int
}

// CreateProject stages the project.yaml + summary.md write under the global
// flock and returns the hydrated *domain.Project. Slug uniqueness is checked
// by walking the projects dir before staging — race-safe because the global
// flock is held for the staged commit.
func (s *Service) CreateProject(ctx context.Context, slug, name, description, summary string) (*domain.Project, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}

	slug = strings.TrimSpace(slug)
	name = strings.TrimSpace(name)
	if !projectSlugRE.MatchString(slug) {
		return nil, fmt.Errorf("%w: slug %q does not match ^[a-z0-9][a-z0-9_-]{0,62}[a-z0-9]$", domain.ErrInvalidArgument, slug)
	}
	if name == "" {
		return nil, fmt.Errorf("%w: name required", domain.ErrInvalidArgument)
	}
	trimmedSummary := strings.TrimSpace(summary)
	if len(trimmedSummary) < minProjectSummaryLen {
		return nil, fmt.Errorf("%w: summary must be at least %d characters of meaningful project context", domain.ErrInvalidArgument, minProjectSummaryLen)
	}

	// Slug uniqueness — cheap walk; the global flock during commit prevents
	// two concurrent creates from racing past this check.
	var conflict bool
	if err := s.Store.WalkProjects(func(existing string, _ *store.ProjectRecord) error {
		if existing == slug {
			conflict = true
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk projects: %w", err)
	}
	if conflict {
		return nil, fmt.Errorf("%w: project %q already exists", domain.ErrAlreadyExists, slug)
	}

	now := time.Now()
	rec := &store.ProjectRecord{
		ID:               uuid.NewString(),
		Slug:             slug,
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
	if err := op.Write(filepath.Join("projects", slug, "project.yaml"), yamlBytes); err != nil {
		return nil, err
	}
	if err := op.Write(filepath.Join("projects", slug, "summary.md"), []byte(ensureTrailingNewline(summary))); err != nil {
		return nil, err
	}
	caption := fmt.Sprintf("create project %s", slug)
	if err := op.Commit(ctx, store.LockGlobal, agent, caption); err != nil {
		return nil, fmt.Errorf("commit create project: %w", err)
	}

	// Async embed: project summary → resident SummaryIdx. Fire-and-forget;
	// dropped jobs get picked up by backfill on the next boot.
	if s.Worker != nil {
		s.Worker.Enqueue(worker.Job{
			Kind:        worker.JobProjectSummary,
			SourcePath:  filepath.Join(s.Store.Root, "projects", slug, "summary.md"),
			SidecarPath: filepath.Join(s.Store.Root, "projects", slug, "summary.embedding.json"),
			EntryID:     rec.ID,
			Owner:       slug,
			Text:        summary,
		})
	}

	proj := &domain.Project{
		ID:          rec.ID,
		Slug:        rec.Slug,
		Name:        rec.Name,
		Description: rec.Description,
		Summary:     summary,
		CreatedBy:   &domain.AgentRef{ID: agent.ID, Name: agent.Name},
		CreatedAt:   rec.CreatedAt,
		UpdatedAt:   rec.UpdatedAt,
	}
	return proj, nil
}

// GetProject returns the project matching idOrSlug. Lazy-loads via cache on
// first read. Read-only — does NOT call requireSession.
func (s *Service) GetProject(ctx context.Context, idOrSlug string) (*domain.Project, error) {
	lp, _, err := s.Cache.Get(ctx, idOrSlug)
	if err != nil {
		return nil, err
	}
	lp.Lock.RLock()
	defer lp.Lock.RUnlock()
	// Return a shallow copy so callers can't accidentally mutate cached
	// state without a lock.
	cp := *lp.Project
	return &cp, nil
}

// ListProjects returns lightweight Project summaries for every project on
// disk. Does NOT lazy-load — projects not already in the cache are read
// off disk directly so listing can't unexpectedly populate the cache.
func (s *Service) ListProjects(ctx context.Context) ([]*domain.Project, error) {
	out := make([]*domain.Project, 0)
	err := s.Store.WalkProjects(func(slug string, rec *store.ProjectRecord) error {
		summary, err := s.Store.ReadProjectSummary(slug)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		p := &domain.Project{
			ID:          rec.ID,
			Slug:        rec.Slug,
			Name:        rec.Name,
			Description: rec.Description,
			Summary:     summary,
			CreatedAt:   rec.CreatedAt,
			UpdatedAt:   rec.UpdatedAt,
		}
		if rec.CreatedByAgentID != nil {
			if agentRec, err := s.Store.ReadAgent(*rec.CreatedByAgentID); err == nil {
				p.CreatedBy = &domain.AgentRef{ID: agentRec.ID, Name: agentRec.Name}
			} else {
				p.CreatedBy = &domain.AgentRef{ID: *rec.CreatedByAgentID}
			}
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	_ = ctx
	return out, nil
}

// UpdateProject mutates name/description/summary on a project. Summary edits
// trigger re-embedding (T10). The cache entry is mutated in place under its
// write lock so subsequent Gets see the new state without a disk re-read.
func (s *Service) UpdateProject(ctx context.Context, idOrSlug string, in domain.UpdateProjectInput) (*domain.Project, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}

	lp, _, err := s.Cache.Get(ctx, idOrSlug)
	if err != nil {
		return nil, err
	}
	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	// Validate summary length before any disk work.
	var newSummary *string
	if in.Summary != nil {
		t := strings.TrimSpace(*in.Summary)
		if len(t) < minProjectSummaryLen {
			return nil, fmt.Errorf("%w: summary must be at least %d characters of meaningful project context", domain.ErrInvalidArgument, minProjectSummaryLen)
		}
		newSummary = in.Summary
	}

	slug := lp.Project.Slug

	// Re-read the on-disk record so we don't drop fields we don't know
	// about (forward-compat: an older binary plus newer yaml shape).
	rec, err := s.Store.ReadProject(slug)
	if err != nil {
		return nil, fmt.Errorf("read project: %w", err)
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
	if err := op.Write(filepath.Join("projects", slug, "project.yaml"), yamlBytes); err != nil {
		return nil, err
	}
	if newSummary != nil {
		if err := op.Write(filepath.Join("projects", slug, "summary.md"), []byte(ensureTrailingNewline(*newSummary))); err != nil {
			return nil, err
		}
	}
	caption := fmt.Sprintf("update project %s", slug)
	if err := op.Commit(ctx, store.LockProject(slug), agent, caption); err != nil {
		return nil, fmt.Errorf("commit update project: %w", err)
	}

	// Mutate the cached project in place. Lock is held above.
	lp.Project.Name = rec.Name
	lp.Project.Description = rec.Description
	lp.Project.UpdatedAt = rec.UpdatedAt
	if newSummary != nil {
		lp.Project.Summary = *newSummary
		// Re-embed the summary so SearchProjects reflects the edit.
		if s.Worker != nil {
			s.Worker.Enqueue(worker.Job{
				Kind:        worker.JobProjectSummary,
				SourcePath:  filepath.Join(s.Store.Root, "projects", slug, "summary.md"),
				SidecarPath: filepath.Join(s.Store.Root, "projects", slug, "summary.embedding.json"),
				EntryID:     rec.ID,
				Owner:       slug,
				Text:        *newSummary,
			})
		}
	}

	cp := *lp.Project
	return &cp, nil
}

// DeleteProject removes a project — refuses if any ticket is non-done.
// Goes through StageOp.RemovePath so the deletion shares the audit trail
// and atomicity model with the rest of the writes (no raw os.RemoveAll).
func (s *Service) DeleteProject(ctx context.Context, idOrSlug string) error {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return err
	}

	lp, _, err := s.Cache.Get(ctx, idOrSlug)
	if err != nil {
		return err
	}

	// Read snapshot of the slug + active counts under the lock, then drop
	// the lock before staging — Cache.Evict re-acquires c.mu.
	lp.Lock.RLock()
	slug := lp.Project.Slug
	active := 0
	for _, t := range lp.Tickets {
		if t.Column != domain.ColumnDone {
			active++
		}
	}
	lp.Lock.RUnlock()

	if active > 0 {
		return fmt.Errorf("%w: project %s has %d active (non-done) ticket(s); resolve them first", domain.ErrFailedPrecondition, slug, active)
	}

	// Drop from cache (closes the watcher) before the StageOp so the
	// fsnotify event from the upcoming RemovePath doesn't try to flip
	// Stale on a doomed entry.
	s.Cache.Evict(slug)

	// Drain pending embed jobs so the worker doesn't write a sidecar into
	// the project dir we're about to RemovePath. Without this, RemoveAll
	// can race a concurrent sidecar write and leave the project dir
	// non-empty / partially-removed.
	if s.Worker != nil {
		s.Worker.Flush(ctx)
	}

	op, err := s.Store.BeginOp()
	if err != nil {
		return err
	}
	defer op.Abort()
	if err := op.RemovePath(filepath.Join("projects", slug)); err != nil {
		return err
	}
	caption := fmt.Sprintf("delete project %s", slug)
	if err := op.Commit(ctx, store.LockGlobal, agent, caption); err != nil {
		return fmt.Errorf("commit delete project: %w", err)
	}
	return nil
}

// LoadProject explicitly pre-warms the cache for the given project. The
// returned LoadProjectResult carries a diagnostic Handle, ExpiresAt
// (LastAccessAt + idle TTL), and ticket-count snapshots. Callers can hand
// the handle off to a `who_am_i` / `loaded_projects` introspection tool.
func (s *Service) LoadProject(ctx context.Context, idOrSlug string) (LoadProjectResult, error) {
	lp, handle, err := s.Cache.Load(ctx, idOrSlug)
	if err != nil {
		return LoadProjectResult{}, err
	}
	lp.Lock.RLock()
	defer lp.Lock.RUnlock()

	active := 0
	for _, t := range lp.Tickets {
		if t.Column != domain.ColumnDone {
			active++
		}
	}
	cp := *lp.Project
	return LoadProjectResult{
		Project:           &cp,
		Handle:            handle,
		ExpiresAt:         lp.LastAccessAt.Add(s.Cache.IdleTTL()),
		TicketCount:       len(lp.Tickets),
		ActiveTicketCount: active,
	}, nil
}

// ensureTrailingNewline appends \n to s if it doesn't already end with one.
// Mirrors the convention store.WriteMarkdown uses for body+summary files.
func ensureTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

// Compile-time check that LoadedProject from the cache package is
// re-exported to anything that needs it without an import-cycle excuse.
var _ *cache.LoadedProject = (*cache.LoadedProject)(nil)
