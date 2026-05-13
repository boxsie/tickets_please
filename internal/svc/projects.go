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
	info, statErr := os.Stat(repoPath)
	switch {
	case statErr == nil:
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: repo_path %s is not a directory", domain.ErrInvalidArgument, repoPath)
		}
	case errors.Is(statErr, fs.ErrNotExist):
		// The bootstrap escape valve for HTTP clients: the caller's
		// project_path is a string identifier that may name a directory on
		// the client's machine, not the server's. Materialise it under the
		// configured RemoteProjectRoot so the path can serve as a stable
		// project identifier for subsequent register_agent calls. Stdio
		// callers whose local repo path actually exists never hit this
		// branch.
		root := strings.TrimSpace(s.Cfg.RemoteProjectRoot)
		if root == "" {
			return nil, fmt.Errorf("%w: repo_path %s: %v", domain.ErrInvalidArgument, repoPath, statErr)
		}
		if !pathUnderRoot(repoPath, root) {
			return nil, fmt.Errorf("%w: repo_path %s does not exist and is not under remote_project_root %s — pass a path under that root, or reconfigure with --remote-project-root", domain.ErrInvalidArgument, repoPath, root)
		}
		if err := os.MkdirAll(repoPath, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", repoPath, err)
		}
	default:
		return nil, fmt.Errorf("%w: repo_path %s: %v", domain.ErrInvalidArgument, repoPath, statErr)
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

	// Async embed: project summary → mount's SummaryIdx. Fire-and-forget;
	// dropped jobs get picked up by backfill on the next boot.
	if mount := s.mountForSlug(slug); mount != nil && mount.Worker != nil {
		mount.Worker.Enqueue(worker.Job{
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
	cp, embedChanged, err := s.updateProjectLocked(ctx, lp, st, agent, in)
	if err != nil {
		return nil, err
	}
	// If the embed_provider or embed_model changed, kick off a wipe + rebuild
	// so the mount's indexes/sidecars realign with the new identity. The
	// project.yaml write has already committed at this point — if the rebuild
	// fails (typically a probe error: user picked a model Ollama hasn't pulled
	// yet) we surface that error to the caller verbatim. The yaml stays
	// written so a follow-up `ollama pull` + Re-embed (or a service restart)
	// realises the swap. The mount's existing Embed/Worker/indexes are left
	// untouched on probe failure (rebuildMountEmbedAssets builds the new
	// provider before swapping), so search keeps working with the old model
	// until the user resolves the underlying issue.
	if embedChanged {
		if err := s.ReembedProject(ctx, cp.Slug); err != nil {
			return cp, err
		}
	}
	return cp, nil
}

// updateProjectLocked is the inner half of UpdateProject — runs under the
// per-project Lock and returns whether the embed identity changed so the
// caller can fire ReembedProject after dropping the lock (ReembedProject
// re-acquires Cache.Get's RLock, so it can't run while we hold the write
// lock here).
func (s *Service) updateProjectLocked(ctx context.Context, lp *cache.LoadedProject, st *store.Store, agent *domain.Agent, in domain.UpdateProjectInput) (*domain.Project, bool, error) {
	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	// Validate summary length before any disk work.
	var newSummary *string
	if in.Summary != nil {
		if err := requireSummary("summary", *in.Summary); err != nil {
			return nil, false, err
		}
		newSummary = in.Summary
	}

	slug := lp.Project.Slug

	// Re-read the on-disk record so we don't drop fields we don't know
	// about (forward-compat: an older binary plus newer yaml shape).
	rec, err := st.ReadProject(slug)
	if err != nil {
		return nil, false, fmt.Errorf("read project: %w", err)
	}
	embedChanged := false
	if in.Name != nil {
		rec.Name = normalizeLabel(*in.Name)
	}
	if in.Description != nil {
		rec.Description = normalizeLabel(*in.Description)
	}
	if in.EmbedProvider != nil {
		newP := strings.TrimSpace(*in.EmbedProvider)
		if newP != rec.EmbedProvider {
			embedChanged = true
		}
		rec.EmbedProvider = newP
	}
	if in.EmbedModel != nil {
		newM := strings.TrimSpace(*in.EmbedModel)
		if newM != rec.EmbedModel {
			embedChanged = true
		}
		rec.EmbedModel = newM
	}
	rec.UpdatedAt = time.Now()

	yamlBytes, err := store.MarshalYAML(rec)
	if err != nil {
		return nil, false, err
	}

	op, err := st.BeginOp()
	if err != nil {
		return nil, false, err
	}
	defer op.Abort()
	if err := op.Write("project.yaml", yamlBytes); err != nil {
		return nil, false, err
	}
	if newSummary != nil {
		if err := op.Write("summary.md", []byte(ensureTrailingNewline(*newSummary))); err != nil {
			return nil, false, err
		}
	}
	caption := fmt.Sprintf("update project %s", slug)
	if err := op.Commit(ctx, store.LockProject(slug), agent, caption); err != nil {
		return nil, false, fmt.Errorf("commit update project: %w", err)
	}

	// Mutate the cached project in place. Lock is held above.
	lp.Project.Name = rec.Name
	lp.Project.Description = rec.Description
	lp.Project.UpdatedAt = rec.UpdatedAt
	if newSummary != nil {
		lp.Project.Summary = *newSummary
		// Re-embed the summary so the mount's SummaryIdx reflects the edit.
		// (Skipped when embed_provider/embed_model also changed — the caller
		// will trigger a full ReembedProject which handles the summary too.)
		if !embedChanged {
			if mount := s.mountForSlug(slug); mount != nil && mount.Worker != nil {
				mount.Worker.Enqueue(worker.Job{
					Kind:        worker.JobProjectSummary,
					SourcePath:  filepath.Join(st.Root, "summary.md"),
					SidecarPath: filepath.Join(st.Root, "summary.embedding.json"),
					EntryID:     rec.ID,
					Owner:       slug,
					Text:        *newSummary,
				})
			}
		}
	}

	cp := *lp.Project
	return &cp, embedChanged, nil
}

// ReembedProject wipes every `*.embedding.json` sidecar for the given project
// and re-enqueues every source file via the mount's embed worker. The call
// returns immediately — the worker drains async. Used by the Settings page
// (W5) and the MCP tool (W3-T2) to recover from corrupted sidecars or to
// realize an embed_provider/embed_model change made via UpdateProject (which
// auto-triggers this path) or by hand-editing project.yaml.
//
// If project.yaml's (embed_provider, embed_model) differs from the mount's
// currently-cached pair, the mount's Embed/indexes/Worker are torn down and
// rebuilt at the new dim before the walk + hydrate; this is the load-bearing
// "switched models, including different dims" recovery path.
func (s *Service) ReembedProject(ctx context.Context, idOrSlug string) error {
	ctx, _, err := s.requireSession(ctx)
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

	lp.Lock.RLock()
	slug := lp.Project.Slug
	lp.Lock.RUnlock()

	// Re-read the on-disk project.yaml so we observe any embed_provider/
	// embed_model edit that was just persisted (UpdateProject writes yaml
	// before calling us; manual yaml edits also flow through here).
	rec, err := st.ReadProject(slug)
	if err != nil {
		return fmt.Errorf("read project: %w", err)
	}

	mount := s.mountForSlug(slug)
	if mount == nil {
		return fmt.Errorf("svc: project %q not mounted", slug)
	}

	if s.Logger != nil {
		s.Logger.Info("reembed: starting", "slug", slug,
			"embed_provider", rec.EmbedProvider, "embed_model", rec.EmbedModel)
	}

	// If the yaml's (provider, model) drifted from the mount's cached pair,
	// rebuild Embed/indexes/Worker at the new dim. Hold mountsMu for the
	// swap so concurrent search/index reads can't observe a half-swapped
	// state. Lock is dropped before the long-running walk + hydrate.
	view := embedViewFromCfg(s.Cfg, rec.EmbedProvider, rec.EmbedModel)
	s.mountsMu.Lock()
	needRebuild := mount.Embed == nil ||
		mount.Embed.Name() != view.Provider ||
		mount.EmbedModel != view.Model
	if needRebuild {
		if err := s.rebuildMountEmbedAssets(mount, rec); err != nil {
			s.mountsMu.Unlock()
			return err
		}
	}
	s.mountsMu.Unlock()

	// Flush BEFORE the destructive walk so no in-flight job can write a
	// sidecar back into the doomed-set after we've removed it. Generalizes
	// the "Flush before tree-removal" rule from DeleteTicket / DeletePhase
	// to "Flush before any destructive sidecar walk".
	if mount.Worker != nil {
		mount.Worker.Flush(ctx)
	}

	// Take the project lock for the duration of the walk + remove. Serializes
	// against concurrent UpdateProject / Delete* on this slug. We don't use a
	// StageOp because sidecars are gitignored — there's no audit trail to
	// preserve and no commit caption that would make sense.
	if err := st.WithProjectLock(ctx, slug, func() error {
		return s.removeAllSidecars(slug, st)
	}); err != nil {
		return fmt.Errorf("reembed walk: %w", err)
	}

	// Re-enqueue everything via the mount's worker. hydrateMount's
	// upsertOrEnqueue branch handles the missing-sidecar case by reading
	// source text and submitting a Job — which is exactly what we want.
	s.hydrateMount(slug, mount)
	if s.Logger != nil {
		s.Logger.Info("reembed: enqueued; worker draining async", "slug", slug)
	}
	return nil
}

// removeAllSidecars walks the project tree and os.Removes every
// `*.embedding.json` sibling of every source file. Missing-file is swallowed
// (errors.Is(fs.ErrNotExist)); other errors warn-log but continue — we'd
// rather get a partial wipe than abort midway and leave a half-deleted state.
func (s *Service) removeAllSidecars(slug string, st *store.Store) error {
	log := s.Logger
	rm := func(path string) {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if log != nil {
				log.Warn("reembed: remove sidecar failed", "slug", slug, "path", path, "err", err)
			}
		}
	}

	// Project summary sidecar.
	if err := st.WalkProjects(func(_ string, _ *store.ProjectRecord) error {
		rm(filepath.Join(st.ProjectDir(slug), "summary.embedding.json"))
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("walk projects: %w", err)
	}

	// Phase summary sidecars.
	if err := st.WalkPhases(slug, func(rec *store.PhaseRecord) error {
		dirName := fmt.Sprintf("%03d-%s", rec.Number, rec.Slug)
		rm(filepath.Join(st.PhaseDir(slug, dirName), "summary.embedding.json"))
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("walk phases: %w", err)
	}

	// Ticket body + learnings sidecars + comment sidecars.
	if err := st.WalkTickets(slug, func(ticketDir, _ string, _ *store.TicketRecord) error {
		rm(filepath.Join(ticketDir, "body.embedding.json"))
		rm(filepath.Join(ticketDir, "learnings.embedding.json"))
		commentsDir := filepath.Join(ticketDir, "comments")
		entries, err := os.ReadDir(commentsDir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if log != nil {
				log.Warn("reembed: read comments dir failed", "slug", slug, "dir", commentsDir, "err", err)
			}
			return nil
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".embedding.json") {
				rm(filepath.Join(commentsDir, name))
			}
		}
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("walk tickets: %w", err)
	}
	return nil
}

// ReembedFailure is one entry in the slice ReembedAllProjects returns
// alongside the queued count. Slug identifies the project that failed; Err
// is the underlying ReembedProject error, typically a probe failure (user
// picked a model Ollama hasn't pulled yet). The web layer renders these
// into a "queued for N; failed for M: <slug>: <err>; ..." flash.
type ReembedFailure struct {
	Slug string
	Err  error
}

// ReembedAllProjects iterates every cached mount and calls ReembedProject on
// each. Returns the count of projects successfully queued plus the list of
// per-project failures. Iteration continues across failures — best-effort
// reembed across all mounts is more useful than aborting on the first stuck
// project.
func (s *Service) ReembedAllProjects(ctx context.Context) (int, []ReembedFailure) {
	type slugMount struct{ slug string }
	var slugs []slugMount
	_ = s.WalkProjectMounts(func(slug string, _ *ProjectMount) error {
		slugs = append(slugs, slugMount{slug: slug})
		return nil
	})
	queued := 0
	var failures []ReembedFailure
	for _, sm := range slugs {
		if err := s.ReembedProject(ctx, sm.slug); err != nil {
			failures = append(failures, ReembedFailure{Slug: sm.slug, Err: err})
			if s.Logger != nil {
				s.Logger.Warn("reembed all: project failed", "slug", sm.slug, "err", err)
			}
			continue
		}
		queued++
	}
	return queued, failures
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
	if mount := s.mountForSlug(slug); mount != nil && mount.Worker != nil {
		mount.Worker.Flush(ctx)
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
// pathUnderRoot reports whether p resolves to a location at or below root.
// Both inputs are cleaned; non-absolute root disables the check (returns
// false) since "under" is meaningless for a relative anchor.
func pathUnderRoot(p, root string) bool {
	root = strings.TrimSpace(root)
	p = strings.TrimSpace(p)
	if root == "" || p == "" {
		return false
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(p) {
		return false
	}
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

func ensureTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

// Compile-time check that LoadedProject from the cache package is
// re-exported to anything that needs it without an import-cycle excuse.
var _ *cache.LoadedProject = (*cache.LoadedProject)(nil)
