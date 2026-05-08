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
	SummaryIdx   *vecindex.Index
	TicketsIdx   *vecindex.Index
	LearningsIdx *vecindex.Index
	CommentsIdx  *vecindex.Index
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
	Logger     *slog.Logger
	Cfg        config.Config

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

	// Worker is the async embedding goroutine. Handlers Enqueue jobs after
	// their StageOp commits; the worker drains the queue, writes the JSON
	// sidecar, and Upserts into the right resident index. Per-mount workers
	// land in W2-T2; for now the single global worker writes into the
	// fallback resident indexes (defaultIndexes) which the registry-empty
	// stdio fallback search consults.
	Worker *worker.Worker

	// defaultIndexes are the resident vec indexes used by the global Worker
	// and consulted as the registry-empty stdio fallback by search RPCs.
	// Per-mount indexes on ProjectMount shadow these for any registered
	// project. Sized at startup to the server-default EmbedDim.
	defaultIndexes worker.Indexes

	// cacheCancel stops the background eviction goroutine. Held so tests
	// (and future graceful-shutdown paths) can tear it down.
	cacheCancel context.CancelFunc

	// cancelWorker stops the embedding worker (and the boot backfill
	// goroutine). Held so Close can drain in-flight jobs cleanly.
	cancelWorker context.CancelFunc

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
		return nil, fmt.Errorf("svc: probe embed provider %q: %w", provider.Name(), err)
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

	st, err := store.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("svc: build store: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	indexes := worker.Indexes{
		Summaries: vecindex.New(),
		Tickets:   vecindex.New(),
		Learnings: vecindex.New(),
		Comments:  vecindex.New(),
	}
	w := worker.New(provider, indexes, 256, logger)
	w.SetModel(cfg.OllamaModel)

	evictCtx, cancelCache := context.WithCancel(context.Background())
	workerCtx, cancelWorker := context.WithCancel(context.Background())

	s := &Service{
		Store:          st,
		AgentStore:     as,
		Logger:         logger,
		Cfg:            cfg,
		Embed:          provider,
		EmbedDim:       embedDim,
		EmbedNew:       factory,
		Worker:         w,
		defaultIndexes: indexes,
		cacheCancel:    cancelCache,
		cancelWorker:   cancelWorker,
		touchOnce:      make(map[string]time.Time),
		projectMounts:  make(map[string]*ProjectMount),
	}

	// Per-mount index routing: when the worker dequeues a job, look up the
	// owner-slug's mount and write into its per-mount index. Falls back to
	// defaultIndexes (the worker's own Indexes) when no mount is registered
	// for the slug — that's the registry-empty stdio bootstrap path.
	w.SetIndexResolver(s.workerIndexResolver)

	// Cache resolves Stores via service-owned closures so a single ProjectCache
	// can serve multiple project mounts. In single-store stdio mode the
	// closures fall back to s.Store regardless of slug.
	pc := cache.New(cache.Resolvers{
		ResolveStore:   s.cacheResolveStore,
		WalkAllStores:  s.cacheWalkAllStores,
		FsnotifyEnabled: cfg.FsnotifyEnabled,
	}, as, cfg)
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
	go s.Worker.Run(workerCtx)

	// Boot backfill: enqueue any source files lacking sidecars. Runs async
	// so an empty / freshly-cloned data dir doesn't pay the cost on every
	// startup, and so a slow Ollama doesn't block service readiness.
	bf := worker.NewBackfiller(st, w, logger)
	go func() {
		if err := bf.Run(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("embed backfill failed", "err", err)
		}
	}()

	return s, nil
}

// Close stops background goroutines (cache evictor + embed worker) and
// releases all watcher resources. It blocks until the worker has drained
// any in-flight jobs so a caller can safely tear down the data dir afterward
// without racing the worker's sidecar writes.
//
// Safe to call multiple times.
func (s *Service) Close() {
	if s.cancelWorker != nil {
		s.cancelWorker()
		s.cancelWorker = nil
		if s.Worker != nil {
			s.Worker.Wait()
		}
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
	s.projectMounts[rec.Slug] = mount
	s.maybeEvictLocked(rec.Slug)
	// Populate resident indexes from this project's on-disk sidecars (and
	// enqueue missing ones via the embed worker). Done with the lock still
	// held — hydrate only touches mount-level indexes + the embed worker
	// queue, neither of which loops back into mountsMu.
	s.hydrateMount(rec.Slug, mount)
	s.persistMountRegistry(repoPath, true)
	return rec.Slug, nil
}

// attachMountEmbedAssets builds (or reuses) the mount's embed.Provider and the
// four resident vec indexes, sized to that provider's probed dim. Provider
// build falls back to the server default when the project record's
// embed_provider is blank or when the factory errors (with a warn log) — a
// project that mis-configures its embedder shouldn't lock the user out of
// their data; per-project re-embed (W3-T1) lets them recover.
//
// Re-mount of a previously-evicted entry skips re-probing the provider when
// it survived eviction (only indexes get nilled there); we just re-allocate
// the four indexes at the previously-probed dim.
func (s *Service) attachMountEmbedAssets(mount *ProjectMount, rec *store.ProjectRecord) error {
	if mount.Embed != nil && mount.SummaryIdx != nil {
		// Fully populated; nothing to do.
		return nil
	}
	if mount.Embed == nil {
		provider, dim, err := s.buildMountProvider(rec.EmbedProvider, rec.EmbedModel)
		if err != nil {
			// Soft fallback: log and use the server default. Search/embed
			// will still work; sidecars will be stamped with the server
			// provider's identity.
			if s.Logger != nil {
				s.Logger.Warn("svc: per-mount embed build failed; falling back to server default",
					"slug", rec.Slug, "embed_provider", rec.EmbedProvider, "embed_model", rec.EmbedModel, "err", err)
			}
			provider = s.Embed
			dim = s.EmbedDim
		}
		mount.Embed = provider
		mount.EmbedDim = dim
	}
	mount.SummaryIdx = vecindex.New()
	mount.TicketsIdx = vecindex.New()
	mount.LearningsIdx = vecindex.New()
	mount.CommentsIdx = vecindex.New()
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
		m.Store = nil
		// Nil the per-mount indexes (same shape Store is nilled in) and
		// drop any defaultIndexes entries tagged with this slug. The
		// freed []float32 vectors don't keep memory alive for an evicted
		// project; ResolveProjectStore rebuilds + rehydrates on next
		// access. Embed and EmbedDim survive on the mount so the fast
		// re-mount can reuse the probed dim without another round-trip.
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

// workerIndexResolver maps a worker Job's (kind, owner-slug) to the right
// per-mount *vecindex.Index. Returns nil for an unknown slug so the worker
// falls back to its own Indexes (the stdio bootstrap path). Read of
// mount.*Idx happens under mountsMu so it can't race with the eviction
// path that nils them out.
func (s *Service) workerIndexResolver(kind worker.JobKind, owner string) *vecindex.Index {
	if owner == "" {
		return nil
	}
	s.mountsMu.Lock()
	defer s.mountsMu.Unlock()
	mount, ok := s.projectMounts[owner]
	if !ok || mount == nil {
		return nil
	}
	switch kind {
	case worker.JobProjectSummary:
		return mount.SummaryIdx
	case worker.JobTicketBody:
		return mount.TicketsIdx
	case worker.JobTicketLearnings:
		return mount.LearningsIdx
	case worker.JobComment:
		return mount.CommentsIdx
	}
	return nil
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
