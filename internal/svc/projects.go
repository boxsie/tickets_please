package svc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/cache"
	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/worker"
)

// defaultEmbedFor returns the (provider, model) pair to stamp into a freshly
// created project record. Falls back to ollama when cfg.EmbedProvider is unset
// so brand-new projects always carry concrete values.
func defaultEmbedFor(cfg config.Config) (string, string) {
	provider := cfg.EmbedProvider
	if provider == "" {
		provider = "ollama"
	}
	switch provider {
	case "openai":
		return provider, "text-embedding-3-small"
	default:
		return provider, cfg.OllamaModel
	}
}

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

// CreateProject is the legacy path-implicit constructor: writes through
// s.Store (whichever Store cfg.DataDir resolved at service-start time) and
// post-mounts under cfg.DataDir's parent if the convention applies. It's
// retained for backward compat with the web handler and most tests, where
// the data dir is fixed at process start.
//
// HTTP/MCP callers should use CreateProjectAt — the server has no cwd to
// derive a destination from, so the project path must be explicit. Both
// paths share the same auth-soft bootstrap behaviour.
func (s *Service) CreateProject(ctx context.Context, slug, name, description, summary string) (*domain.Project, error) {
	return s.createProjectImpl(ctx, "", s.Store, slug, name, description, summary)
}

// CreateProjectAt writes the project under <repoPath>/.tickets_please/ and
// mounts it there. Used by the MCP create_project tool: the HTTP server has
// no cwd, so the LLM must declare the destination. Auth-soft like the legacy
// path — no session required, created_by left empty for the bootstrap call.
//
// repoPath must be an absolute directory path. .tickets_please/ is created
// inside it if missing (mkdir -p semantics).
func (s *Service) CreateProjectAt(ctx context.Context, repoPath, slug, name, description, summary string) (*domain.Project, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return nil, fmt.Errorf("%w: repo_path required", domain.ErrInvalidArgument)
	}
	if !filepath.IsAbs(repoPath) {
		return nil, fmt.Errorf("%w: repo_path %q must be absolute", domain.ErrInvalidArgument, repoPath)
	}
	if info, err := os.Stat(repoPath); err != nil {
		return nil, fmt.Errorf("%w: repo_path %s: %v", domain.ErrInvalidArgument, repoPath, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("%w: repo_path %s is not a directory", domain.ErrInvalidArgument, repoPath)
	}

	dataDir := filepath.Join(repoPath, ".tickets_please")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dataDir, err)
	}
	stagingDir := filepath.Join(dataDir, ".staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", stagingDir, err)
	}

	st, err := s.buildMountStore(dataDir)
	if err != nil {
		return nil, err
	}

	return s.createProjectImpl(ctx, repoPath, st, slug, name, description, summary)
}

// createProjectImpl is the shared body. repoPath empty + targetStore == s.Store
// is the legacy CreateProject path; repoPath set + targetStore == path-specific
// is the explicit CreateProjectAt path.
//
// Stages the project.yaml + summary.md write under targetStore's global flock
// and returns the hydrated *domain.Project. Slug uniqueness is checked by
// walking targetStore before staging — race-safe because the global flock is
// held for the staged commit.
//
// Auth-soft: if a session is on the context it gets attributed as created_by;
// if not, the project lands with no creator and the auto-commit is skipped.
// This is the single bootstrap escape valve.
func (s *Service) createProjectImpl(ctx context.Context, repoPath string, targetStore *store.Store, slug, name, description, summary string) (*domain.Project, error) {
	ctx, agent, err := s.optionalSession(ctx)
	if err != nil {
		return nil, err
	}

	slug = strings.TrimSpace(slug)
	name = normalizeLabel(name)
	if err := requireSlug("slug", slug); err != nil {
		return nil, err
	}
	if err := requireNonEmptyTrimmed("name", name); err != nil {
		return nil, err
	}
	if err := requireSummary("summary", summary); err != nil {
		return nil, err
	}

	// Single-project-per-Store invariant (post-flatten): a `project.yaml` at
	// the data-dir root means *some* project already lives here. We reject
	// any create — same-slug or otherwise — because writing would overwrite
	// the existing record.
	var existingSlug string
	if err := targetStore.WalkProjects(func(existing string, _ *store.ProjectRecord) error {
		existingSlug = existing
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk projects: %w", err)
	}
	if existingSlug != "" {
		return nil, fmt.Errorf("%w: project %q already exists at %s (one project per data dir)", domain.ErrAlreadyExists, existingSlug, targetStore.Root)
	}

	now := time.Now()
	provider, model := defaultEmbedFor(s.Cfg)
	rec := &store.ProjectRecord{
		ID:            uuid.NewString(),
		Slug:          slug,
		Name:          name,
		Description:   normalizeLabel(description),
		EmbedProvider: provider,
		EmbedModel:    model,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if agent != nil {
		rec.CreatedByAgentID = &agent.ID
	}
	yamlBytes, err := store.MarshalYAML(rec)
	if err != nil {
		return nil, err
	}

	op, err := targetStore.BeginOp()
	if err != nil {
		return nil, err
	}
	defer op.Abort()
	if err := op.Write("project.yaml", yamlBytes); err != nil {
		return nil, err
	}
	if err := op.Write("summary.md", []byte(ensureTrailingNewline(summary))); err != nil {
		return nil, err
	}
	caption := fmt.Sprintf("create project %s", slug)
	if err := op.Commit(ctx, store.LockGlobal, agent, caption); err != nil {
		return nil, fmt.Errorf("commit create project: %w", err)
	}

	// Mount the freshly-created project so list_projects, register_agent, and
	// every per-slug routing path can find it.
	//
	// CreateProjectAt path: repoPath is set explicitly — mount there.
	// CreateProject (legacy) path: repoPath is empty; fall back to the old
	// convention check (cfg.DataDir's parent) which only fires when DataDir
	// follows the `<repo>/.tickets_please` shape (prod stdio dogfood).
	mountPath := repoPath
	if mountPath == "" && targetStore != nil && filepath.Base(targetStore.Root) == ".tickets_please" {
		mountPath = filepath.Dir(targetStore.Root)
	}
	if mountPath != "" {
		if _, regErr := s.RegisterProjectMount(ctx, mountPath); regErr != nil && s.Logger != nil {
			s.Logger.Warn("svc: post-create register mount failed", "repo", mountPath, "err", regErr)
		}
	}

	// Async embed: project summary → resident SummaryIdx. Fire-and-forget;
	// dropped jobs get picked up by backfill on the next boot.
	if s.Worker != nil {
		s.Worker.Enqueue(worker.Job{
			Kind:        worker.JobProjectSummary,
			SourcePath:  filepath.Join(targetStore.Root, "summary.md"),
			SidecarPath: filepath.Join(targetStore.Root, "summary.embedding.json"),
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
		CreatedAt:   rec.CreatedAt,
		UpdatedAt:   rec.UpdatedAt,
	}
	if agent != nil {
		proj.CreatedBy = &domain.AgentRef{ID: agent.ID, Name: agent.Name}
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

// ListProjects returns lightweight Project summaries for every project the
// service knows about — including those mounted from per-repo data dirs via
// register_agent, not just whatever lives in the central s.Store. Does NOT
// lazy-load: projects not already in the cache are read off disk directly so
// listing can't unexpectedly populate the cache.
func (s *Service) ListProjects(ctx context.Context) ([]*domain.Project, error) {
	out := make([]*domain.Project, 0)
	seenSlug := make(map[string]struct{})
	err := s.cacheWalkAllStores(func(st *store.Store) error {
		return st.WalkProjects(func(slug string, rec *store.ProjectRecord) error {
			// Distinct stores must not surface the same slug twice; defensive
			// dedupe so a misconfigured mount + matching central project
			// doesn't double-count.
			if _, dup := seenSlug[slug]; dup {
				return nil
			}
			seenSlug[slug] = struct{}{}
			summary, err := st.ReadProjectSummary(slug)
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
				if agentRec, err := s.AgentStore.ReadAgent(*rec.CreatedByAgentID); err == nil {
					p.CreatedBy = &domain.AgentRef{ID: agentRec.ID, Name: agentRec.Name}
				} else {
					p.CreatedBy = &domain.AgentRef{ID: *rec.CreatedByAgentID}
				}
			}
			out = append(out, p)
			return nil
		})
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
	st, err := s.ResolveProjectStore(ctx, lp.Project.Slug)
	if err != nil {
		return nil, err
	}
	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	// Validate summary length before any disk work.
	var newSummary *string
	if in.Summary != nil {
		if err := requireSummary("summary", *in.Summary); err != nil {
			return nil, err
		}
		newSummary = in.Summary
	}

	slug := lp.Project.Slug

	// Re-read the on-disk record so we don't drop fields we don't know
	// about (forward-compat: an older binary plus newer yaml shape).
	rec, err := st.ReadProject(slug)
	if err != nil {
		return nil, fmt.Errorf("read project: %w", err)
	}
	if in.Name != nil {
		rec.Name = normalizeLabel(*in.Name)
	}
	if in.Description != nil {
		rec.Description = normalizeLabel(*in.Description)
	}
	if in.EmbedProvider != nil {
		rec.EmbedProvider = strings.TrimSpace(*in.EmbedProvider)
	}
	if in.EmbedModel != nil {
		rec.EmbedModel = strings.TrimSpace(*in.EmbedModel)
	}
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
	if err := op.Write("project.yaml", yamlBytes); err != nil {
		return nil, err
	}
	if newSummary != nil {
		if err := op.Write("summary.md", []byte(ensureTrailingNewline(*newSummary))); err != nil {
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
				SourcePath:  filepath.Join(st.Root, "summary.md"),
				SidecarPath: filepath.Join(st.Root, "summary.embedding.json"),
				EntryID:     rec.ID,
				Owner:       slug,
				Text:        *newSummary,
			})
		}
	}

	cp := *lp.Project
	return &cp, nil
}

// DeleteProject removes a project unconditionally — including any active
// (non-done) tickets, phases, comments, and embeddings. Per-ticket
// "completion is sacred" only constrains individual ticket lifecycle; at
// the project level the user owns the whole tree and can nuke it.
//
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
	st, err := s.ResolveProjectStore(ctx, lp.Project.Slug)
	if err != nil {
		return err
	}

	// Take a snapshot of the slug under the lock, then drop the lock before
	// staging — Cache.Evict re-acquires c.mu.
	lp.Lock.RLock()
	slug := lp.Project.Slug
	lp.Lock.RUnlock()

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

	op, err := st.BeginOp()
	if err != nil {
		return err
	}
	defer op.Abort()
	// Flat layout: a project's contents are siblings at the data-dir root,
	// so we remove each project-owned path individually rather than nuking
	// the data dir itself (which also holds agents/, .staging/, .lock).
	for _, rel := range []string{"project.yaml", "summary.md", "summary.embedding.json", "phases", "tickets"} {
		if err := op.RemovePath(rel); err != nil {
			return err
		}
	}
	caption := fmt.Sprintf("delete project %s", slug)
	if err := op.Commit(ctx, store.LockGlobal, agent, caption); err != nil {
		return fmt.Errorf("commit delete project: %w", err)
	}

	// Drop the in-memory mount + the persistent registry entry. Both fail
	// silently — the on-disk delete already succeeded so we shouldn't
	// surface a registry-write error to the caller.
	s.mountsMu.Lock()
	mount, hadMount := s.projectMounts[slug]
	delete(s.projectMounts, slug)
	s.mountsMu.Unlock()
	if hadMount && mount != nil {
		s.persistMountRegistry(mount.RepoPath, false)
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
