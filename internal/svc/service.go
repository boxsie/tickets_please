// Package svc implements the in-process Service API surface that the MCP
// transport (T12) and any future gRPC/HTTP transport call into. T15 owns the
// canonical Service struct + constructor; later tickets append their own
// fields and constructor wiring without replacing the type.
package svc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"tickets_please/internal/cache"
	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/embed"
	"tickets_please/internal/eventbus"
	"tickets_please/internal/store"
	"tickets_please/internal/vecindex"
	"tickets_please/internal/worker"
)

// defaultMaxLoadedProjects mirrors cache.New's fallback for the same field;
// the registry's LRU eviction uses it when cfg.MaxLoadedProjects is zero.
const defaultMaxLoadedProjects = 16

// ProjectMount is one slug-keyed entry in the Service's project registry. The
// Store may be nil when the entry has been LRU-evicted — RepoPath is retained
// so ResolveProjectStore can re-mount silently on the next access. ProjectID
// is captured at registration to detect "same slug, different project" repos
// trying to claim the same mount key.
//
// Each mount carries its own embed.Provider built from project.yaml's
// embed_provider/embed_model (with server defaults filling blanks) so projects
// using different models can coexist; EmbedDim is the probed-from-this-provider
// dim (per-mount; shadows Service.EmbedDim). The four vec indexes are sized
// to that dim — search routes through them.
type ProjectMount struct {
	Store         *store.Store
	RepoPath      string
	ProjectID     string
	LastTouchedAt time.Time

	Embed        embed.Provider
	EmbedDim     int
	EmbedModel   string // sidecar identity stamp; survives eviction
	SummaryIdx   *vecindex.Index
	TicketsIdx   *vecindex.Index
	LearningsIdx *vecindex.Index
	CommentsIdx  *vecindex.Index

	// Worker is the per-mount embedding goroutine. Owns its own queue and
	// writes only into this mount's four indexes. Eviction calls Stop and
	// nils this pointer; tree-removal paths call Flush before BeginOp so a
	// pending sidecar write doesn't race a RemovePath. Reads of this field
	// must hold mountsMu — same contract as the index pointers.
	Worker *worker.Worker

	// Feedback is the per-project likes/dislikes/retrievals aggregate store
	// backing the W1 search-feedback loop. Nil on a mount whose Store hasn't
	// been built yet (LRU-evicted entries); RegisterProjectMount and the
	// re-mount path in ResolveProjectStore both rebuild it.
	Feedback *store.FeedbackStore

	// QualityParams is the per-project tuning of the W2 search-ranking
	// multiplier (α, β, min_multiplier, enabled). Sourced from project.yaml's
	// `feedback` block at mount-attach time; defaults to α=β=2, min=0.5,
	// enabled=true when the block is absent.
	QualityParams QualityParams

	// ArchivePolicy is the per-project tuning of the W3 archive sweep.
	// Sourced from project.yaml's `archive` block at mount-attach time;
	// defaults to enabled=false (opt-in) with the canonical thresholds.
	ArchivePolicy ArchivePolicy

	// sweepInFlight coalesces concurrent ApplyArchivePolicy auto-sweeps on
	// this mount: a second background sweep started before the first
	// finishes is a no-op-with-warning. Manual `apply_archive_policy` calls
	// don't take this — they're explicit user actions.
	sweepInFlight sync.Mutex
}

// Service is the in-process API surface. T15 declared the foundational
// fields; W2-T1 of the per-project-embedders phase moved the embedder +
// vec indexes onto each ProjectMount so different projects can run
// different models simultaneously.
type Service struct {
	// Store is the "default" project Store used by stdio mode (where cfg.DataDir
	// points at the one repo's .tickets_please/). In multi-project HTTP mode
	// this is still set when cfg.DataDir was provided, but the registry is the
	// canonical lookup; per-call routing should go through ResolveProjectStore.
	Store      *store.Store
	AgentStore *store.AgentStore
	// UserStore is the central web-UI user registry (W2). Shares the agent
	// store's data root + global lock; consumed by the OAuth login flow to
	// upsert users on successful authentication.
	UserStore *store.UserStore
	// MembershipStore is the per-project (user, project) → role registry (W2).
	// Read by the web layer's route guards to authorize access; written by the
	// invitation / role-management flows (W2-4) and bootstrap admin (W2-6).
	MembershipStore *store.MembershipStore
	Logger          *slog.Logger
	Cfg             config.Config

	// publisher receives typed realtime events on every mutation (move,
	// complete, comment, archive, agent register/seen). Fire-and-forget: the
	// web layer's eventbus.Bus implements it; stdio mode and tests leave it as
	// the no-op default so call sites never nil-check. Set via SetPublisher.
	publisher eventbus.Publisher

	// Cache is the in-memory project cache (T04). Lazy-loads project trees
	// off disk, sliding-TTL evicts, and listens for cross-process file
	// changes via fsnotify.
	Cache *cache.ProjectCache

	// Embed is the server-default embedding Provider. Used as the fallback
	// when a mount doesn't override (or before any mount is built). Per-mount
	// providers shadow this for project-specific search/embedding work.
	Embed embed.Provider

	// EmbedDim is the dimensionality of the server-default Embed. Per-mount
	// providers carry their own probed Dim on ProjectMount.EmbedDim.
	EmbedDim int

	// EmbedNew is the factory that builds a provider for a mount from a
	// per-project EmbedConfig view. Defaults to embed.New; tests override
	// to inject deterministic fakes without touching real Ollama/OpenAI.
	EmbedNew func(embed.EmbedConfig) (embed.Provider, error)

	// defaultIndexes are the resident vec indexes consulted as the
	// registry-empty stdio fallback by search RPCs. No worker writes here
	// in W2-T2 — every mount owns its own four indexes and its own worker.
	// These exist purely so unscoped search RPCs (registry empty) can
	// degrade gracefully without crashing on nil indexes.
	defaultIndexes worker.Indexes

	// cacheCancel stops the background eviction goroutine. Held so tests
	// (and future graceful-shutdown paths) can tear it down.
	cacheCancel context.CancelFunc

	// backfillCancel stops the boot backfill goroutine.
	backfillCancel context.CancelFunc

	// bgCtx is the background context (shared with the boot backfill) handed to
	// async embed-model acquisition goroutines so Close cancels them. bgWG
	// tracks those goroutines so Close blocks until they unwind before workers
	// are stopped. See startEnsureMountModel / ensureMountModelAsync.
	bgCtx context.Context
	bgWG  sync.WaitGroup

	// Agent debounce state. touchOnce tracks the last time we rewrote
	// LastSeenAt for a given agent id; touchMu guards the map.
	touchOnce map[string]time.Time
	touchMu   sync.Mutex

	// Project mount registry. mountsMu guards mutations to projectMounts and
	// LRU eviction; reads under the lock are cheap because the map is small
	// (<= cfg.MaxLoadedProjects, default 16).
	mountsMu      sync.Mutex
	projectMounts map[string]*ProjectMount
}

// SetPublisher wires the realtime event sink. Called once at serve startup
// with the web layer's eventbus.Bus. Safe to leave unset (stdio/tests) — the
// Nop default discards events.
func (s *Service) SetPublisher(p eventbus.Publisher) {
	if p == nil {
		p = eventbus.Nop{}
	}
	s.publisher = p
}

// publish fans a typed event to the wired publisher, fire-and-forget. A nil
// publisher can't happen (Nop default) but we guard anyway so a future
// zero-valued Service in a test doesn't panic.
func (s *Service) publish(ev eventbus.Event) {
	if s.publisher == nil {
		return
	}
	s.publisher.Publish(ev)
}

// New builds a Service: resolves the data dir into a *store.Store, wires a
// JSON-handler slog logger pointed at stderr, builds the project cache, the
// server-default embedding provider, the resident fallback vec indexes, and
// the async embed worker. The dim check happens BEFORE the worker starts so
// a misconfigured provider fails loud rather than silently writing
// mismatched sidecars.
//
// The boot backfill walk runs in its own goroutine — startup never blocks on
// embedding latency.
func New(cfg config.Config) (*Service, error) {
	provider, err := embed.New(embedViewFromCfg(cfg, "", ""))
	if err != nil {
		return nil, fmt.Errorf("svc: build embed provider: %w", err)
	}
	return newServiceCore(cfg, provider, embed.New)
}

// embedViewFromCfg builds an embed.EmbedConfig from cfg, optionally overriding
// the (provider, model) pair from a project.yaml. Empty overrides fall back to
// the server defaults so partially-configured projects still work.
func embedViewFromCfg(cfg config.Config, provider, model string) embed.EmbedConfig {
	if provider == "" {
		provider = cfg.EmbedProvider
	}
	if model == "" {
		// Pick the model that pairs with the resolved provider. When the
		// project record overrides only the provider (no model), the cfg's
		// OllamaModel is the right default for ollama; OpenAI ignores model.
		switch provider {
		case "openai":
			model = "text-embedding-3-small"
		default:
			model = cfg.OllamaModel
		}
	}
	return embed.EmbedConfig{
		Provider:  provider,
		Model:     model,
		OllamaURL: cfg.OllamaURL,
		OpenAIKey: cfg.OpenAIKey,
	}
}

// NewWithEmbed is the same as New but lets the caller inject an
// embed.Provider. Tests use this to drop in a deterministic fake without
// contacting a real Ollama / OpenAI server. The same provider is also used
// as the per-mount factory's return value so per-project mount build never
// dials out either; tests that want different per-mount providers should
// override Service.EmbedNew after construction.
//
// The provider is probed once before the worker starts; whatever Dim() reports
// after probe is what the indexes and hydrate use.
func NewWithEmbed(cfg config.Config, provider embed.Provider) (*Service, error) {
	if provider == nil {
		return nil, fmt.Errorf("svc: nil embed provider")
	}
	factory := func(_ embed.EmbedConfig) (embed.Provider, error) { return provider, nil }
	return newServiceCore(cfg, provider, factory)
}

// newServiceCore is the shared body that New and NewWithEmbed delegate to.
// The caller passes the server-default Provider plus the per-mount factory
// — production wires factory=embed.New (per-project models picked from yaml);
// tests get a closure that returns the injected fake regardless of view.
func newServiceCore(cfg config.Config, provider embed.Provider, factory func(embed.EmbedConfig) (embed.Provider, error)) (*Service, error) {
	if provider == nil {
		return nil, fmt.Errorf("svc: nil embed provider")
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelProbe()
	if err := provider.Probe(probeCtx); err != nil {
		// Probe no longer pulls (ticket 3a138760), so a missing model surfaces
		// here. The server default is the one model we must have to embed
		// anything at all, so for a genuinely fresh deploy we acquire it once,
		// synchronously — this is the documented one-time install cost, not the
		// per-project regression that blocked the handshake. A non-pullable
		// provider (OpenAI) or any non-missing error is fatal as before.
		ensurer, canPull := provider.(embed.ModelEnsurer)
		if !canPull || !embed.IsModelMissing(err) {
			return nil, fmt.Errorf("svc: probe embed provider %q: %w", provider.Name(), err)
		}
		slog.Default().Info("svc: server-default embed model missing; pulling (one-time, blocks boot)",
			"provider", provider.Name())
		if perr := ensurer.EnsureModel(probeCtx); perr != nil {
			return nil, fmt.Errorf("svc: acquire server-default embed model: %w", perr)
		}
		if err := provider.Probe(probeCtx); err != nil {
			return nil, fmt.Errorf("svc: probe embed provider %q after pull: %w", provider.Name(), err)
		}
	}
	embedDim := provider.Dim()

	// Resolve the central data root. When DataRoot is empty (e.g. in tests that
	// supply only DataDir) fall back to a sibling tempdir-like path so tests
	// never pollute the user's real ~/.tickets_please.
	dataRoot := cfg.DataRoot
	if dataRoot == "" {
		dataRoot = cfg.DataDir + "-central"
	}
	as, err := store.NewAgentStore(dataRoot, cfg.LockTimeoutSeconds)
	if err != nil {
		return nil, fmt.Errorf("svc: build agent store: %w", err)
	}
	us, err := store.NewUserStore(dataRoot, cfg.LockTimeoutSeconds)
	if err != nil {
		return nil, fmt.Errorf("svc: build user store: %w", err)
	}
	ms, err := store.NewMembershipStore(dataRoot, cfg.LockTimeoutSeconds)
	if err != nil {
		return nil, fmt.Errorf("svc: build membership store: %w", err)
	}

	st, err := store.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("svc: build store: %w", err)
	}
	logger := slog.Default()

	indexes := worker.Indexes{
		Summaries: vecindex.New(),
		Tickets:   vecindex.New(),
		Learnings: vecindex.New(),
		Comments:  vecindex.New(),
	}

	evictCtx, cancelCache := context.WithCancel(context.Background())
	backfillCtx, cancelBackfill := context.WithCancel(context.Background())

	s := &Service{
		Store:           st,
		AgentStore:      as,
		UserStore:       us,
		MembershipStore: ms,
		Logger:          logger,
		Cfg:             cfg,
		Embed:           provider,
		EmbedDim:        embedDim,
		EmbedNew:        factory,
		defaultIndexes:  indexes,
		cacheCancel:     cancelCache,
		backfillCancel:  cancelBackfill,
		bgCtx:           backfillCtx,
		publisher:       eventbus.Nop{},
		touchOnce:       make(map[string]time.Time),
		projectMounts:   make(map[string]*ProjectMount),
	}

	// Cache resolves Stores via service-owned closures so a single ProjectCache
	// can serve multiple project mounts. In single-store stdio mode the
	// closures fall back to s.Store regardless of slug.
	pc := cache.New(cache.Resolvers{
		ResolveStore:    s.cacheResolveStore,
		WalkAllStores:   s.cacheWalkAllStores,
		FsnotifyEnabled: cfg.FsnotifyEnabled,
	}, as, us, cfg)
	pc.Logger = logger
	s.Cache = pc

	// Eager-mount the default Store when cfg.DataDir already holds a project.
	// This keeps stdio sessions working: the runtime (cmd/main) builds Service
	// once and immediately calls service-level methods that expect a Store —
	// without an eager mount the first such call would fail with "not mounted".
	if cfg.DataDir != "" {
		if abs, absErr := filepath.Abs(cfg.DataDir); absErr == nil {
			if _, statErr := os.Stat(filepath.Join(abs, "project.yaml")); statErr == nil {
				// cfg.DataDir is `<repo>/.tickets_please`; the repoPath is its parent.
				repoPath := filepath.Dir(abs)
				if _, mErr := s.RegisterProjectMount(context.Background(), repoPath); mErr != nil {
					logger.Warn("svc: eager-mount of default project failed", "repo", repoPath, "err", mErr)
				}
			}
		}
	}

	// Restore mounts persisted across restarts. Each path that fails (repo
	// moved, marker deleted, permission denied) logs a warning but doesn't
	// block startup — the registry is best-effort, the source of truth is
	// each repo's project.yaml. Paths already mounted by the eager-mount
	// block above are deduped by RegisterProjectMount's idempotency.
	if cfg.DataRoot != "" {
		paths, regErr := loadMountRegistry(cfg.DataRoot)
		if regErr != nil {
			logger.Warn("svc: load mount registry failed", "err", regErr)
		}
		for _, p := range paths {
			if _, mErr := s.RegisterProjectMount(context.Background(), p); mErr != nil {
				logger.Warn("svc: restore mount from registry failed", "repo", p, "err", mErr)
			}
		}
	}

	go s.Cache.RunEvictor(evictCtx)

	// Boot backfill: walk every mounted project and enqueue any source files
	// lacking sidecars onto that mount's worker. Runs async so an empty /
	// freshly-cloned data dir doesn't pay the cost on every startup, and so
	// a slow Ollama doesn't block service readiness.
	go s.runBootBackfill(backfillCtx)

	return s, nil
}

// runBootBackfill walks every currently-registered mount and runs a
// Backfiller against it. Each mount's worker drains its own queue.
func (s *Service) runBootBackfill(ctx context.Context) {
	type bfMount struct {
		st *store.Store
		w  *worker.Worker
	}
	var mounts []bfMount
	s.mountsMu.Lock()
	for _, m := range s.projectMounts {
		if m == nil || m.Store == nil || m.Worker == nil {
			continue
		}
		mounts = append(mounts, bfMount{st: m.Store, w: m.Worker})
	}
	s.mountsMu.Unlock()
	for _, bm := range mounts {
		if err := ctx.Err(); err != nil {
			return
		}
		bf := worker.NewBackfiller(bm.st, bm.w, s.Logger)
		if err := bf.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.Logger.Warn("embed backfill failed", "err", err)
		}
	}
}

// Close stops background goroutines (cache evictor + boot backfill + every
// per-mount embed worker) and releases all watcher resources. It blocks until
// every worker has drained any in-flight jobs so a caller can safely tear
// down the data dir afterward without racing sidecar writes.
//
// Safe to call multiple times.
func (s *Service) Close() {
	if s.backfillCancel != nil {
		s.backfillCancel()
		s.backfillCancel = nil
	}
	// Wait for background embed-model acquisition goroutines to unwind before
	// stopping workers — they may be mid-rebuild/hydrate against a mount's
	// worker, and bgCtx is already cancelled (via backfillCancel) so any
	// in-flight pull returns promptly.
	s.bgWG.Wait()
	// Snapshot + Stop every per-mount worker. Use a generous shutdown budget
	// so a slow real provider doesn't trip a goroutine leak in tests.
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.mountsMu.Lock()
	workers := make([]*worker.Worker, 0, len(s.projectMounts))
	for _, m := range s.projectMounts {
		if m != nil && m.Worker != nil {
			workers = append(workers, m.Worker)
		}
	}
	s.mountsMu.Unlock()
	for _, w := range workers {
		w.Stop(stopCtx)
	}
	if s.cacheCancel != nil {
		s.cacheCancel()
		s.cacheCancel = nil
	}
	if s.Cache != nil {
		s.Cache.CloseAll()
	}
}

// maxLoadedProjects returns the configured upper bound for resident project
// mounts, falling back to defaultMaxLoadedProjects when cfg leaves it zero.
func (s *Service) maxLoadedProjects() int {
	if s.Cfg.MaxLoadedProjects > 0 {
		return s.Cfg.MaxLoadedProjects
	}
	return defaultMaxLoadedProjects
}

// RegisterProjectMount validates repoPath, reads its
// `<repoPath>/.tickets_please/project.yaml`, and inserts a ProjectMount keyed
// by the project slug. Idempotent for the (repoPath, project UUID) pair —
// re-registering the same combination only refreshes LastTouchedAt. A slug
// collision against a *different* repo or UUID returns an error.
//
// Mounts beyond cfg.MaxLoadedProjects (default 16) are LRU-evicted: the
// oldest-touched entry has its Store nilled out but its RepoPath retained so
// the next ResolveProjectStore call can re-mount silently. The currently
// inserted mount is exempt from eviction.
func (s *Service) RegisterProjectMount(_ context.Context, repoPath string) (string, error) {
	if repoPath == "" {
		return "", fmt.Errorf("svc: register project mount: repoPath required")
	}
	if !filepath.IsAbs(repoPath) {
		return "", fmt.Errorf("svc: register project mount: repoPath %q must be absolute", repoPath)
	}
	info, err := os.Stat(repoPath)
	if err != nil {
		return "", fmt.Errorf("svc: register project mount: stat %s: %w", repoPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("svc: register project mount: %s is not a directory", repoPath)
	}

	yamlPath := filepath.Join(repoPath, ".tickets_please", "project.yaml")
	rec := &store.ProjectRecord{}
	if err := store.ReadYAML(yamlPath, rec); err != nil {
		return "", fmt.Errorf("svc: register project mount: read %s: %w", yamlPath, err)
	}
	if rec.Slug == "" || rec.ID == "" {
		return "", fmt.Errorf("svc: register project mount: %s missing slug or id", yamlPath)
	}

	dataDir := filepath.Join(repoPath, ".tickets_please")

	s.mountsMu.Lock()
	defer s.mountsMu.Unlock()

	if existing, ok := s.projectMounts[rec.Slug]; ok {
		// Idempotent re-register: same repo path AND same project id → bump
		// touch and return. Anything else under this slug is a conflict.
		if existing.RepoPath == repoPath && existing.ProjectID == rec.ID {
			if existing.Store != nil {
				existing.LastTouchedAt = time.Now()
				s.persistMountRegistry(repoPath, true)
				return rec.Slug, nil
			}
			// Re-mount an evicted entry under the same path.
			st, err := s.buildMountStore(dataDir)
			if err != nil {
				return "", err
			}
			if err := s.attachMountEmbedAssets(existing, rec); err != nil {
				return "", err
			}
			existing.Store = st
			existing.LastTouchedAt = time.Now()
			s.attachMountFeedback(existing, rec.Slug)
			s.maybeEvictLocked(rec.Slug)
			// Re-hydrate the resident indexes for this slug; eviction earlier
			// may have dropped its entries.
			s.hydrateMount(rec.Slug, existing)
			s.persistMountRegistry(repoPath, true)
			return rec.Slug, nil
		}
		return "", fmt.Errorf("svc: slug %q is already mounted at %s", rec.Slug, existing.RepoPath)
	}

	st, err := s.buildMountStore(dataDir)
	if err != nil {
		return "", err
	}
	mount := &ProjectMount{
		Store:         st,
		RepoPath:      repoPath,
		ProjectID:     rec.ID,
		LastTouchedAt: time.Now(),
	}
	if err := s.attachMountEmbedAssets(mount, rec); err != nil {
		return "", err
	}
	s.attachMountFeedback(mount, rec.Slug)
	s.projectMounts[rec.Slug] = mount
	s.maybeEvictLocked(rec.Slug)
	// Populate resident indexes from this project's on-disk sidecars (and
	// enqueue missing ones via the embed worker). Done with the lock still
	// held — hydrate only touches mount-level indexes + the embed worker
	// queue, neither of which loops back into mountsMu.
	s.hydrateMount(rec.Slug, mount)
	s.persistMountRegistry(repoPath, true)
	s.maybeStartAutoSweep(rec.Slug, mount)
	return rec.Slug, nil
}

// maybeStartAutoSweep fires the W3 archive sweep in the background after a
// fresh mount if the project's `archive.auto_sweep_on_mount: true` is set
// AND `archive.enabled: true`. Both flags are opt-in — auto-sweep is
// silently skipped otherwise. Sweep runs through ApplyArchivePolicy with
// commit=true; result is structured-logged with counts.
//
// The per-mount sweepInFlight mutex coalesces overlapping triggers: a second
// trigger arriving before the first finishes is a no-op-with-warning, not a
// stampede. ctx uses s.bgCtx so process shutdown cancels in-flight sweeps.
func (s *Service) maybeStartAutoSweep(slug string, mount *ProjectMount) {
	if mount == nil || !mount.ArchivePolicy.Enabled || !mount.ArchivePolicy.AutoSweepOnMount {
		return
	}
	s.bgWG.Add(1)
	go func() {
		defer s.bgWG.Done()
		if !mount.sweepInFlight.TryLock() {
			if s.Logger != nil {
				s.Logger.Warn("svc: auto-sweep already in flight; skipping duplicate trigger",
					"slug", slug)
			}
			return
		}
		defer mount.sweepInFlight.Unlock()

		ctx := s.bgCtx
		if ctx == nil {
			ctx = context.Background()
		}
		// Auto-sweep doesn't have an authenticated agent session in the
		// traditional sense (no MCP client triggered it). We synthesize one
		// using the project's registered agents via the existing requireSession
		// path — but ApplyArchivePolicy and its downstream ArchiveTicket calls
		// expect a session. The cleanest path: skip the auto-sweep when no
		// session is bindable, log, and let the next manual `apply_archive_policy`
		// run instead. (Hobby-scale: in practice the LLM-driven mount path
		// always has a session.)
		if s.AgentStore == nil {
			if s.Logger != nil {
				s.Logger.Info("svc: auto-sweep skipped — no AgentStore for session attribution",
					"slug", slug)
			}
			return
		}

		started := time.Now()
		report, err := s.ApplyArchivePolicy(ctx, ApplyPolicyInput{
			ProjectIDOrSlug: slug,
			Commit:          true,
		})
		took := time.Since(started)
		if err != nil {
			if s.Logger != nil {
				s.Logger.Warn("svc: auto-sweep failed", "slug", slug, "err", err, "took", took)
			}
			return
		}
		if s.Logger != nil {
			s.Logger.Info("svc: auto-sweep complete",
				"slug", slug,
				"considered", report.Considered,
				"archived", len(report.Archived),
				"skipped", len(report.Skipped),
				"took", took)
		}
	}()
}

// attachMountFeedback (re)loads the per-project feedback store onto mount and
// resolves the QualityParams from project.yaml's optional `feedback` block.
// Logs and clears Feedback on error — a corrupt feedback.yaml shouldn't lock
// the user out of their tickets; mutations through RateSearchResult will
// no-op gracefully when Feedback is nil, and search scoring falls back to
// pure cosine when QualityParams.Enabled ends up false.
func (s *Service) attachMountFeedback(mount *ProjectMount, slug string) {
	if mount == nil || mount.Store == nil || slug == "" {
		return
	}
	fb, err := store.LoadFeedback(mount.Store, slug)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("svc: load feedback store failed; feedback disabled for this mount",
				"slug", slug, "err", err)
		}
		mount.Feedback = nil
	} else {
		mount.Feedback = fb
	}
	// QualityParams + ArchivePolicy: default + per-project override from
	// project.yaml's `feedback` and `archive` blocks. Missing project.yaml
	// or missing blocks → defaults.
	params := defaultQualityParams()
	policy := defaultArchivePolicy()
	rec := &store.ProjectRecord{}
	if err := store.ReadYAML(filepath.Join(mount.Store.Root, "project.yaml"), rec); err == nil {
		params = resolveQualityParams(rec.Feedback)
		policy = resolveArchivePolicy(rec.Archive)
	}
	mount.QualityParams = params
	mount.ArchivePolicy = policy
}

// resolveQualityParams merges a per-project FeedbackConfigRecord onto the
// canonical defaults. Any nil field inherits the default value, so a project
// that only overrides `enabled: false` (kill switch) keeps α=β=2, min=0.5.
func resolveQualityParams(rec *store.FeedbackConfigRecord) QualityParams {
	out := defaultQualityParams()
	if rec == nil {
		return out
	}
	if rec.Alpha != nil {
		out.Alpha = *rec.Alpha
	}
	if rec.Beta != nil {
		out.Beta = *rec.Beta
	}
	if rec.MinMultiplier != nil {
		out.MinMultiplier = *rec.MinMultiplier
	}
	if rec.Enabled != nil {
		out.Enabled = *rec.Enabled
	}
	return out
}

// attachMountEmbedAssets builds (or reuses) the mount's embed.Provider, the
// four resident vec indexes (sized to that provider's probed dim), and the
// per-mount embed Worker. Provider build falls back to the server default
// when the project record's embed_provider is blank or when the factory
// errors (with a warn log) — a project that mis-configures its embedder
// shouldn't lock the user out of their data; per-project re-embed (W3-T1)
// lets them recover.
//
// Re-mount of a previously-evicted entry skips re-probing the provider when
// it survived eviction (only indexes + worker get nilled there); we just
// re-allocate the four indexes and a fresh worker.
func (s *Service) attachMountEmbedAssets(mount *ProjectMount, rec *store.ProjectRecord) error {
	if mount.Embed != nil && mount.SummaryIdx != nil && mount.Worker != nil {
		// Fully populated; nothing to do.
		return nil
	}
	if mount.Embed == nil {
		provider, dim, err := s.buildMountProvider(rec.EmbedProvider, rec.EmbedModel)
		if err != nil {
			if s.Logger != nil {
				s.Logger.Warn("svc: per-mount embed build failed; falling back to server default",
					"slug", rec.Slug, "embed_provider", rec.EmbedProvider, "embed_model", rec.EmbedModel, "err", err)
			}
			mount.Embed = s.Embed
			mount.EmbedDim = s.EmbedDim
			// Truth, not intent (ticket de1a552e): stamp the model we ACTUALLY
			// embed with — the server default — not the project's requested
			// model. The old code stamped the requested model here, so sidecars
			// written during the fallback window lied about their provenance
			// and the staleness check couldn't tell they needed rebuilding once
			// the requested model arrived.
			mount.EmbedModel = embedViewFromCfg(s.Cfg, "", "").Model
			// If the only problem is that the requested model isn't pulled yet,
			// acquire it in the background and swap the mount over (re-embedding
			// under the real model) when it lands — never block this attach
			// path on the pull (ticket 3a138760). Other failures (unknown
			// provider, missing OpenAI key) are permanent: stay on the fallback.
			if embed.IsModelMissing(err) {
				s.startEnsureMountModel(&store.ProjectRecord{
					ID:            rec.ID,
					Slug:          rec.Slug,
					EmbedProvider: rec.EmbedProvider,
					EmbedModel:    rec.EmbedModel,
				})
			}
		} else {
			mount.Embed = provider
			mount.EmbedDim = dim
			view := embedViewFromCfg(s.Cfg, rec.EmbedProvider, rec.EmbedModel)
			mount.EmbedModel = view.Model
		}
	}
	s.allocMountIndexesAndWorker(mount)
	return nil
}

// allocMountIndexesAndWorker (re)allocates fresh empty indexes and a fresh
// embed Worker for the mount, using whatever Embed/EmbedModel the mount
// currently carries. Caller is responsible for stopping any pre-existing
// worker before invoking this — otherwise the goroutine leaks.
func (s *Service) allocMountIndexesAndWorker(mount *ProjectMount) {
	mount.SummaryIdx = vecindex.New()
	mount.TicketsIdx = vecindex.New()
	mount.LearningsIdx = vecindex.New()
	mount.CommentsIdx = vecindex.New()
	mount.Worker = worker.New(context.Background(), mount.Embed, mount.EmbedModel, worker.Indexes{
		Summaries: mount.SummaryIdx,
		Tickets:   mount.TicketsIdx,
		Learnings: mount.LearningsIdx,
		Comments:  mount.CommentsIdx,
	}, 256, s.Logger)
}

// rebuildMountEmbedAssets is the destructive variant used by ReembedProject
// when project.yaml's embed_provider/embed_model differ from the mount's
// currently-cached pair. It builds + probes the new embed.Provider FIRST,
// and only on success stops the old worker and re-allocates the four
// indexes + a fresh worker around the new provider.
//
// On probe failure the mount is left fully intact (its existing
// Embed/Worker/indexes survive untouched) and the error is returned so the
// caller can surface it to the user — the previous "fall back to server
// default" branch silently lied about the swap and is gone.
//
// Caller must hold mountsMu so concurrent reads of mount.Embed/Worker/etc.
// don't observe a half-swapped state.
func (s *Service) rebuildMountEmbedAssets(mount *ProjectMount, rec *store.ProjectRecord) error {
	if mount == nil {
		return fmt.Errorf("svc: rebuild mount embed assets: nil mount")
	}
	// Build the new provider BEFORE touching the existing assets. If the probe
	// fails (e.g. user typed `bge-m3` before `ollama pull bge-m3`), we leave
	// the mount in its pre-rebuild state and return the error verbatim.
	provider, dim, err := s.buildMountProvider(rec.EmbedProvider, rec.EmbedModel)
	if err != nil {
		return err
	}
	// New provider is healthy — now it's safe to stop the old worker so its
	// goroutine doesn't keep writing into soon-to-be-orphaned indexes. A
	// short budget; on timeout we drop the worker anyway.
	if mount.Worker != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		mount.Worker.Stop(stopCtx)
		cancel()
		mount.Worker = nil
	}
	mount.Embed = provider
	mount.EmbedDim = dim
	view := embedViewFromCfg(s.Cfg, rec.EmbedProvider, rec.EmbedModel)
	mount.EmbedModel = view.Model
	s.allocMountIndexesAndWorker(mount)
	return nil
}

// buildMountProvider constructs and probes a fresh embed.Provider from the
// per-project (provider, model) override, with server cfg filling the gaps.
// Returns the provider plus its probed dim. A nil EmbedNew falls back to
// embed.New (production path); tests inject deterministic fakes via EmbedNew.
func (s *Service) buildMountProvider(provider, model string) (embed.Provider, int, error) {
	view := embedViewFromCfg(s.Cfg, provider, model)
	factory := s.EmbedNew
	if factory == nil {
		factory = embed.New
	}
	p, err := factory(view)
	if err != nil {
		return nil, 0, fmt.Errorf("build embed provider: %w", err)
	}
	if p == nil {
		return nil, 0, fmt.Errorf("embed factory returned nil provider for %q", view.Provider)
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := p.Probe(probeCtx); err != nil {
		return nil, 0, fmt.Errorf("probe embed provider %q: %w", p.Name(), err)
	}
	return p, p.Dim(), nil
}

// startEnsureMountModel launches the background acquisition of a mount's
// requested-but-missing embed model. Non-blocking by design: the boot /
// mount-attach path returns immediately on the server-default fallback, and
// this goroutine swaps the real provider in once the model lands. Tracked by
// bgWG so Close waits for it before tearing workers down.
//
// rec must be a caller-owned copy (the attach path passes a fresh
// ProjectRecord) so the goroutine never races a mutation of the original.
func (s *Service) startEnsureMountModel(rec *store.ProjectRecord) {
	s.bgWG.Add(1)
	go func() {
		defer s.bgWG.Done()
		s.ensureMountModelAsync(rec)
	}()
}

// ensureMountModelAsync pulls rec's embed model, then (on success) swaps the
// mount off the server-default fallback onto a real provider for that model
// and re-embeds the project so its sidecars carry truthful, correctly-
// dimensioned vectors. Every failure mode degrades to "stay on the fallback"
// with a warning — a project must never become unreachable because a model
// pull failed.
func (s *Service) ensureMountModelAsync(rec *store.ProjectRecord) {
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}
	ctx := s.bgCtx
	if ctx == nil {
		ctx = context.Background()
	}
	slug := rec.Slug

	factory := s.EmbedNew
	if factory == nil {
		factory = embed.New
	}
	view := embedViewFromCfg(s.Cfg, rec.EmbedProvider, rec.EmbedModel)
	p, err := factory(view)
	if err != nil {
		log.Warn("svc: background embed-model ensure: build provider failed; staying on fallback",
			"slug", slug, "embed_model", view.Model, "err", err)
		return
	}
	ensurer, ok := p.(embed.ModelEnsurer)
	if !ok {
		// Nothing to self-acquire (e.g. OpenAI) — fallback is permanent.
		return
	}

	log.Info("svc: per-project embed model missing; acquiring in background",
		"slug", slug, "embed_model", view.Model)
	pullCtx, cancel := context.WithTimeout(ctx, embed.ModelPullTimeout)
	defer cancel()
	if err := ensurer.EnsureModel(pullCtx); err != nil {
		log.Warn("svc: background embed-model pull failed; staying on server-default fallback",
			"slug", slug, "embed_model", view.Model, "err", err)
		return
	}

	mount := s.mountForSlug(slug)
	if mount == nil {
		return // unmounted / evicted while we pulled — nothing to swap
	}
	s.mountsMu.Lock()
	swapErr := s.rebuildMountEmbedAssets(mount, rec)
	s.mountsMu.Unlock()
	if swapErr != nil {
		log.Warn("svc: background embed swap failed after pull; staying on fallback",
			"slug", slug, "embed_model", view.Model, "err", swapErr)
		return
	}

	// Wipe the fallback-stamped sidecars and re-embed under the real model so
	// search stops returning server-default-dimensioned vectors. Mirrors the
	// flush → wipe → hydrate sequence ReembedProject uses.
	if mount.Worker != nil {
		mount.Worker.Flush(ctx)
	}
	if st := mount.Store; st != nil {
		if err := st.WithProjectLock(ctx, slug, func() error {
			return s.removeAllSidecars(slug, st)
		}); err != nil {
			log.Warn("svc: background re-embed sidecar wipe failed", "slug", slug, "err", err)
		}
	}
	s.hydrateMount(slug, mount)
	log.Info("svc: per-project embed model ready; re-embedded under correct model",
		"slug", slug, "embed_model", view.Model)
}

// persistMountRegistry writes the add/remove to <DataRoot>/registry.yaml so
// the mount survives a restart. Best-effort — failures log but don't bubble
// up because the registry is a hint for the next boot, not authoritative
// state. Caller normally holds mountsMu; the on-disk registry is written by
// at most one process at a time so no flock is needed.
func (s *Service) persistMountRegistry(repoPath string, add bool) {
	if s.Cfg.DataRoot == "" {
		return
	}
	var err error
	if add {
		err = addToMountRegistry(s.Cfg.DataRoot, repoPath)
	} else {
		err = removeFromMountRegistry(s.Cfg.DataRoot, repoPath)
	}
	if err != nil && s.Logger != nil {
		op := "add"
		if !add {
			op = "remove"
		}
		s.Logger.Warn("svc: registry "+op+" failed", "repo", repoPath, "err", err)
	}
}

// ResolveProjectStore returns the live *store.Store for the given slug,
// re-mounting silently from RepoPath if the entry exists but was previously
// LRU-evicted. Returns an error when the slug was never registered.
func (s *Service) ResolveProjectStore(_ context.Context, slug string) (*store.Store, error) {
	if slug == "" {
		return nil, fmt.Errorf("svc: resolve project store: slug required")
	}

	s.mountsMu.Lock()
	defer s.mountsMu.Unlock()

	mount, ok := s.projectMounts[slug]
	if !ok {
		// Single-store stdio fallback: if the default Store hosts this slug,
		// register-on-demand from its on-disk project.yaml so the registry and
		// the default store stay in lockstep.
		if s.Store != nil {
			rec := &store.ProjectRecord{}
			if err := store.ReadYAML(filepath.Join(s.Store.Root, "project.yaml"), rec); err == nil {
				if rec.Slug == slug {
					m := &ProjectMount{
						Store:         s.Store,
						RepoPath:      filepath.Dir(s.Store.Root),
						ProjectID:     rec.ID,
						LastTouchedAt: time.Now(),
					}
					if err := s.attachMountEmbedAssets(m, rec); err != nil {
						return nil, err
					}
					s.attachMountFeedback(m, slug)
					s.projectMounts[slug] = m
					return s.Store, nil
				}
			}
		}
		return nil, fmt.Errorf("svc: project %q not mounted; call register_agent first", slug)
	}

	rehydrated := false
	if mount.Store == nil {
		// Re-mount silently from the retained RepoPath.
		dataDir := filepath.Join(mount.RepoPath, ".tickets_please")
		st, err := s.buildMountStore(dataDir)
		if err != nil {
			return nil, fmt.Errorf("svc: re-mount project %q: %w", slug, err)
		}
		// Eviction nilled the four indexes (Embed/EmbedDim survive); just
		// re-allocate fresh empty indexes at the same dim. A change to the
		// project's embed_provider/embed_model in the yaml between
		// eviction and re-mount won't be picked up here — that's a future
		// re-embed flow's job (W3-T1), not silent re-probe at resolve time.
		rec := &store.ProjectRecord{Slug: slug}
		if err := s.attachMountEmbedAssets(mount, rec); err != nil {
			return nil, fmt.Errorf("svc: re-attach embed assets for %q: %w", slug, err)
		}
		mount.Store = st
		rehydrated = true
	}
	mount.LastTouchedAt = time.Now()
	s.maybeEvictLocked(slug)
	if rehydrated {
		// Eviction dropped the feedback store along with the resident indexes;
		// rebuild both so post-eviction reads see the on-disk state.
		s.attachMountFeedback(mount, slug)
		// Eviction nuked the resident-index entries for this slug; refill
		// them from disk so search results return for this project again.
		s.hydrateMount(slug, mount)
	}
	return mount.Store, nil
}

// WalkProjectMounts iterates over every registered project mount and invokes
// fn. The iteration order is unspecified. fn returning a non-nil error stops
// the walk and the error is returned to the caller.
func (s *Service) WalkProjectMounts(fn func(slug string, mount *ProjectMount) error) error {
	// Snapshot to avoid holding mountsMu across the callback (which may call
	// back into ResolveProjectStore / RegisterProjectMount).
	s.mountsMu.Lock()
	snap := make(map[string]*ProjectMount, len(s.projectMounts))
	for slug, m := range s.projectMounts {
		snap[slug] = m
	}
	s.mountsMu.Unlock()
	for slug, m := range snap {
		if err := fn(slug, m); err != nil {
			return err
		}
	}
	return nil
}

// buildMountStore constructs a *store.Store rooted at dataDir, reusing the
// service config's auto-commit / lock / fsnotify knobs. Caller normally holds
// mountsMu but the store constructor itself takes no shared state, so this is
// also safe to call without the lock.
func (s *Service) buildMountStore(dataDir string) (*store.Store, error) {
	cfg := config.Config{
		DataDir:            dataDir,
		AutoCommit:         s.Cfg.AutoCommit,
		LockTimeoutSeconds: s.Cfg.LockTimeoutSeconds,
		FsnotifyEnabled:    s.Cfg.FsnotifyEnabled,
	}
	st, err := store.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("svc: build project store at %s: %w", dataDir, err)
	}
	return st, nil
}

// maybeEvictLocked applies LRU eviction when the count of *resident* (non-nil
// Store) mounts exceeds the configured cap. The freshly-touched slug `keep`
// is always exempt from eviction. Caller must hold mountsMu.
//
// Eviction nils out the Store but retains the ProjectMount entry so the
// RepoPath survives — ResolveProjectStore re-mounts on the next access.
func (s *Service) maybeEvictLocked(keep string) {
	limit := s.maxLoadedProjects()
	type entry struct {
		slug string
		when time.Time
	}
	var resident []entry
	for slug, m := range s.projectMounts {
		if m.Store == nil {
			continue
		}
		resident = append(resident, entry{slug, m.LastTouchedAt})
	}
	if len(resident) <= limit {
		return
	}
	sort.Slice(resident, func(i, j int) bool { return resident[i].when.Before(resident[j].when) })
	over := len(resident) - limit
	for i := 0; i < len(resident) && over > 0; i++ {
		if resident[i].slug == keep {
			continue
		}
		m := s.projectMounts[resident[i].slug]
		if m == nil || m.Store == nil {
			continue
		}
		// Stop the per-mount worker before nilling pointers so the goroutine
		// doesn't leak. Use a short budget; a stuck provider getting cut off
		// here is preferable to holding mountsMu indefinitely.
		if m.Worker != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			m.Worker.Stop(stopCtx)
			cancel()
			m.Worker = nil
		}
		m.Store = nil
		m.Feedback = nil
		s.dropMountFromIndexes(resident[i].slug)
		over--
	}
}

// cacheResolveStore is the cache.Resolvers.ResolveStore closure. Falls back to
// s.Store (the default single-mount stdio store) when the registry has no
// entry for the slug — keeps existing single-project tests + stdio bootstrap
// working without each test path having to register a mount.
func (s *Service) cacheResolveStore(slug string) (*store.Store, error) {
	s.mountsMu.Lock()
	if mount, ok := s.projectMounts[slug]; ok && mount.Store != nil {
		mount.LastTouchedAt = time.Now()
		st := mount.Store
		s.mountsMu.Unlock()
		return st, nil
	}
	s.mountsMu.Unlock()
	if s.Store != nil {
		return s.Store, nil
	}
	return nil, fmt.Errorf("svc: project %q not mounted", slug)
}

// cacheWalkAllStores is the cache.Resolvers.WalkAllStores closure. Used by the
// cache's id→slug fallback walk: it iterates every mounted store, plus the
// default s.Store when present and not already in the registry.
func (s *Service) cacheWalkAllStores(fn func(*store.Store) error) error {
	seen := make(map[*store.Store]struct{})
	s.mountsMu.Lock()
	stores := make([]*store.Store, 0, len(s.projectMounts))
	for _, m := range s.projectMounts {
		if m.Store == nil {
			continue
		}
		stores = append(stores, m.Store)
		seen[m.Store] = struct{}{}
	}
	s.mountsMu.Unlock()
	if s.Store != nil {
		if _, ok := seen[s.Store]; !ok {
			stores = append(stores, s.Store)
		}
	}
	for _, st := range stores {
		if err := fn(st); err != nil {
			return err
		}
	}
	return nil
}

// MountRepoPathForSlug returns the repo path of the project mounted under
// slug, or ("", false) when no mount exists. Used by the MCP register_agent
// handler in remote mode to resolve `project_slug` to its on-disk path
// without forcing the LLM to know server-side filesystem layout.
func (s *Service) MountRepoPathForSlug(slug string) (string, bool) {
	m := s.mountForSlug(slug)
	if m == nil {
		return "", false
	}
	return m.RepoPath, true
}

// mountForSlug returns the mount registered under slug, or nil. Read happens
// under mountsMu so the returned pointer can't be racing eviction (Worker /
// indexes get nil'd while the lock is held). Callers that follow up with
// mount.Worker.Enqueue / Flush after dropping the lock are still safe — the
// worker itself is goroutine-safe and Stop drains in-flight calls, so a
// concurrent Stop simply means the call no-ops.
func (s *Service) mountForSlug(slug string) *ProjectMount {
	if slug == "" {
		return nil
	}
	s.mountsMu.Lock()
	defer s.mountsMu.Unlock()
	return s.projectMounts[slug]
}

// hostStoreForTicket finds the store and project slug that host a ticket id by
// walking every mounted store + the default s.Store. Returns ErrNotFound when
// no store hosts the ticket.
//
// Replacement for the legacy resolveTicketProject which walked s.Store only —
// callers MUST use the returned store for any subsequent ops on this ticket
// (BeginOp / filepath.Join(st.Root, ...)) so mutations land in the same store
// the lookup found rather than blindly into s.Store.
func (s *Service) hostStoreForTicket(id string) (*store.Store, string, error) {
	var hostStore *store.Store
	var hostSlug string
	err := s.cacheWalkAllStores(func(st *store.Store) error {
		if hostSlug != "" {
			return nil
		}
		return st.WalkProjects(func(slug string, _ *store.ProjectRecord) error {
			if hostSlug != "" {
				return nil
			}
			return st.WalkTickets(slug, func(_, _ string, tr *store.TicketRecord) error {
				if tr.ID == id {
					hostStore = st
					hostSlug = slug
				}
				return nil
			})
		})
	})
	if err != nil {
		return nil, "", fmt.Errorf("walk projects: %w", err)
	}
	if hostSlug == "" {
		return nil, "", fmt.Errorf("%w: ticket %s", domain.ErrNotFound, id)
	}
	return hostStore, hostSlug, nil
}
