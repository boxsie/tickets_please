---
id: T04
title: Project methods + project cache
status: TODO
owner: ""
depends_on: [T02, T03, T15]
parallelizable_with: []
wave: 3
files:
  - internal/svc/projects.go
  - internal/cache/projectcache.go
  - internal/svc/service.go
estimate: large
stretch: false
---

# T04 — ProjectService + project cache (lazy + optional load)

## Scope

Implement the project methods on `svc.Service` on top of the filesystem `Store`, plus the in-memory **project cache** with sliding idle TTL eviction. Lazy auto-load on every project op; `LoadProject` is just a perf hint that pre-warms.

**In:** All six methods (Create/Get/List/Update/Delete/Load), `cache.ProjectCache`, eviction goroutine, `svc.Service` struct + `New()` constructor, hooks into the binary's `mcp` subcommand startup.

**Out:** Other entity methods (T05/T06/T07/T16). No embedding hooks (T10 patches markers in this handler). Session attribution depends on T15's middleware.

## Files

- `internal/svc/projects.go` — the project methods on `Service`
- `internal/cache/projectcache.go` — `ProjectCache` with sliding TTL + LRU
- `internal/svc/service.go` — **edited, not created here**. T15 owns this file. T04 extends the struct with `Cache *cache.ProjectCache` and extends `New` to construct it.

## Details

### `ProjectCache`

T04's cache is **vector-free**. It holds the loaded entity tree from disk plus the staleness/watcher plumbing for cross-process coherence. Per-project vector indexes belong to T11 (search) — they get attached to `LoadedProject` later by T10/T11 patches, the same way T08/T09/T10/T11 each contribute fields to `Service`.

```go
type LoadedProject struct {
    Project       *domain.Project
    Phases        map[string]*domain.Phase     // id → phase (built when T16 lands)
    PhasesBySlug  map[string]*domain.Phase
    Tickets       map[string]*domain.Ticket    // id → ticket (any phase or none)
    Comments      map[string][]*domain.Comment // ticket id → ordered comments
    LoadedAt      time.Time
    LastAccessAt  time.Time
    Stale         atomic.Bool                  // set by fsnotify; next Get reloads
    watcher       *store.ProjectWatcher        // nil if fsnotify disabled
    Lock          sync.RWMutex
}

type ProjectCache struct {
    store      *store.Store
    idleTTL    time.Duration
    maxLoaded  int
    mu         sync.Mutex
    loaded     map[string]*LoadedProject  // slug → loaded
    handles    map[string]string          // diagnostic uuid → slug
}

func New(store *store.Store, cfg config.Config) *ProjectCache

func (c *ProjectCache) Get(ctx context.Context, idOrSlug string) (*LoadedProject, string, error)
// Returns the loaded project plus a diagnostic handle. Auto-loads on miss.
// If the existing entry's Stale flag is set, drops it and reloads transparently.

func (c *ProjectCache) Load(ctx context.Context, idOrSlug string) (*LoadedProject, string, error)
// Same as Get; the `LoadProject` service method calls this for explicit pre-warm.

func (c *ProjectCache) MarkAccess(slug string)
// Bump LastAccessAt without a full Get.

func (c *ProjectCache) Evict(slug string)
// Drop from cache, close the watcher, idempotent.

func (c *ProjectCache) RunEvictor(ctx context.Context)
// Goroutine: every 60s, evict projects whose LastAccessAt + idleTTL < now,
// or LRU-evict beyond maxLoaded.
```

**Loading flow:**
1. Acquire `c.mu`.
2. Walk the project's directory once: parses `project.yaml`, `summary.md`, every `tickets/<NNN>-…/ticket.yaml` + `body.md` + (if done) `completion.md`, every `comments/*.md`. Hand-rolled `record → domain` conversion using the helpers in T02.
3. If `cfg.FsnotifyEnabled`, call `store.WatchProject(slug)` and start a goroutine that flips `loaded.Stale` on every event.
4. Insert into `c.loaded`, release `c.mu`.

**Get with stale check:**
```go
func (c *ProjectCache) Get(ctx, slug) (*LoadedProject, string, error) {
    c.mu.Lock()
    lp, ok := c.loaded[slug]
    if ok && lp.Stale.Load() {
        c.evictLocked(slug)  // closes watcher
        ok = false
    }
    c.mu.Unlock()
    if !ok { return c.loadFromDisk(ctx, slug) }
    lp.LastAccessAt = time.Now()
    return lp, c.handleFor(slug), nil
}
```

**Eviction closes the watcher** so we don't leak fsnotify resources. Re-loading sets up a fresh watcher.

T11 (search) attaches a `Vectors *vecindex.Index` field to `LoadedProject` via the same "later ticket adds the field" pattern T15 documented for `Service`. Don't define that field here.

### Extending `Service` (declared by T15)

T15 owns `internal/svc/service.go` with the foundational struct + `New`. T04 *appends* the project-cache field and wires its construction:

```go
type Service struct {
    // ... fields T15 owns (Store, Logger, Cfg, agent state) ...

    // Added by T04:
    Cache *cache.ProjectCache
}

func New(cfg config.Config) (*Service, error) {
    // ... T15 startup ...
    cache := cache.New(store, cfg.ProjectIdleMinutes, cfg.MaxLoadedProjects)
    s.Cache = cache
    go s.Cache.RunEvictor(ctx)
    // ...
}
```

T05/T06/T07/T16 hang off the same `Service` and use `s.Cache.Get(ctx, slug)` whenever they need a loaded project.

T08, T09, T10, T11 will each append their own fields (`Embed`, `LearningsIdx`, `SummaryIdx`, `Worker`) when they land.

### Methods

- **`CreateProject(ctx, slug, name, description, summary)`** — validate `summary >= 200 chars` after trim, slug uniqueness (walk projects), then `StageOp` writing `project.yaml` + `summary.md`. Update `SummaryIdx` enqueue (T10 marker). Returns the new Project.
- **`GetProject(ctx, idOrSlug)`** — `Cache.Get`, return `Project` from `LoadedProject.Project`.
- **`ListProjects(ctx)`** — `WalkProjects` returning lightweight `Project` summaries (loaded or not). Don't trigger lazy-load just for listing.
- **`UpdateProject(ctx, idOrSlug, in UpdateProjectInput)`** — load via cache, mutate `LoadedProject.Project` under its write lock, `StageOp` rewrite of `project.yaml` and (if summary changed) `summary.md`. If summary changed, enqueue `JobProjectSummary` and update `SummaryIdx`.
- **`DeleteProject(ctx, idOrSlug)`** — refuse if project has any non-`done` tickets (`ErrFailedPrecondition`). Otherwise: `Cache.Evict(slug)` (closes the watcher), then a `StageOp` with a single `RemovePath("projects/<slug>")` op committed under the global flock. Auto-commit captures the deletion. **No raw `os.RemoveAll`** — go through StageOp so the audit trail and atomicity model stay consistent.
- **`LoadProject(ctx, idOrSlug) (LoadProjectResult, error)`** — explicit cache pre-warm. Returns `{Project, Handle, ExpiresAt, TicketCount, ActiveTicketCount}`. The `Handle` is purely diagnostic (used by the MCP `who_am_i` / `loaded_projects` tool to show cache state).

Mutating methods start with `s.requireSession(ctx)` (T15) and read the agent via `domain.AgentFrom(ctx)` to populate `created_by`.

### Validation

- `summary` must be ≥ 200 characters after `strings.TrimSpace`. Reject with `InvalidArgument` and a clear message: `summary must be at least 200 characters of meaningful project context`.
- `slug` must match `^[a-z0-9][a-z0-9_-]{0,62}[a-z0-9]$` — server-validated. Conflicts return `AlreadyExists`.

### Auto-commit hooks

Each successful mutation calls `op.Commit(ctx, agent, summary)` with verbs:
- create project: `[tickets_please] create project <slug> [<agent>]`
- update project: `[tickets_please] update project <slug> [<agent>]`
- delete project: `[tickets_please] delete project <slug> [<agent>]`

`agent` comes from the request context (set by T15's session interceptor).

## Acceptance criteria

- [ ] `Service.New(cfg)` boots; project cache started.
- [ ] In a unit test (or via T12's MCP path), `CreateProject` with summary < 200 chars → `ErrInvalidArgument`.
- [ ] `CreateProject` with valid input creates `.tickets_please/projects/<slug>/{project.yaml,summary.md}`. With `auto_commit: true` in a git repo, this produces a single commit attributed to the calling agent.
- [ ] Duplicate slug → `ErrAlreadyExists`.
- [ ] `GetProject` works by UUID and by slug. First call lazy-loads; subsequent calls hit the cache.
- [ ] `LoadProject` returns a populated `LoadProjectResult`; subsequent ops re-use the cached state.
- [ ] After idle > `project_idle_minutes`, the eviction loop drops the project; the next op transparently reloads (logs at info).
- [ ] `UpdateProject(summary=…)` rewrites `summary.md` and triggers a re-embed.
- [ ] `DeleteProject` with active tickets → `ErrFailedPrecondition`.
- [ ] `DeleteProject` happy path goes through `StageOp.RemovePath` (verify by inspecting `.staging/<op-id>/` if forcibly killed mid-delete).
- [ ] With `MAX_LOADED_PROJECTS=2`, loading a third project evicts the LRU.
- [ ] `ListProjects` does NOT trigger lazy loads (verify by checking cache size before/after).
- [ ] Cross-process staleness: write to `project.yaml` from outside the cache (e.g. another goroutine using the Store directly), confirm `LoadedProject.Stale` flips and the next `Get` reloads.
- [ ] Eviction closes the fsnotify watcher (verify by inspecting goroutine count before/after).

## Notes

See **Project loading & in-memory cache**, **Project summary**, and **Service API > Projects** in [`../SPEC.md`](../SPEC.md). T05/T06/T07/T16 all reach into the project cache for their reads/writes — keep `LoadedProject.Lock` exposed and document the lock-ordering convention (always project lock, then store StageOp).
