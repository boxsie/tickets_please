---
id: T04
title: ProjectService + project cache
status: TODO
owner: ""
depends_on: [T02, T03, T15]
parallelizable_with: [T05, T06, T08]
wave: 2
files:
  - internal/svc/service.go
  - internal/svc/projects.go
  - internal/cache/projectcache.go
  - cmd/tickets_please/main.go
estimate: large
stretch: false
---

# T04 — ProjectService + project cache (lazy + optional load)

## Scope

Implement the project methods on `svc.Service` on top of the filesystem `Store`, plus the in-memory **project cache** with sliding idle TTL eviction. Lazy auto-load on every project op; `LoadProject` is just a perf hint that pre-warms.

**In:** All six methods (Create/Get/List/Update/Delete/Load), `cache.ProjectCache`, eviction goroutine, `svc.Service` struct + `New()` constructor, hooks into the binary's `mcp` subcommand startup.

**Out:** Other entity methods (T05/T06/T07/T16). No embedding hooks (T10 patches markers in this handler). Session attribution depends on T15's middleware.

## Files

- `internal/svc/service.go` — `Service` struct (Store, Embed, Cache, Worker, Indexes, Logger); `New(cfg) (*Service, error)`
- `internal/svc/projects.go`
- `internal/cache/projectcache.go` — `ProjectCache` with sliding TTL + LRU
- `cmd/tickets_please/main.go` — `runMCP(cfg)` wires Service → MCP server

## Details

### `ProjectCache`

```go
type LoadedProject struct {
    Project       *domain.Project
    Tickets       map[string]*domain.Ticket    // id → ticket
    Comments      map[string][]*domain.Comment // ticket id → ordered comments
    Vectors       *vecindex.Index              // per-project body+comments index
    LoadedAt      time.Time
    LastAccessAt  time.Time
    Lock          sync.RWMutex
}

type ProjectCache struct {
    store          *store.Store
    embed          embed.Provider
    worker         *worker.Worker
    learningsIdx   *vecindex.Index   // resident, shared
    summaryIdx     *vecindex.Index   // resident, shared
    idleTTL        time.Duration
    maxLoaded      int
    mu             sync.Mutex
    loaded         map[string]*LoadedProject  // slug → loaded
    handles        map[string]string          // handle uuid → slug (for x-project-handle hint)
}

func (c *ProjectCache) Get(ctx context.Context, idOrSlug string) (*LoadedProject, string, error)
// Returns the loaded project plus a fresh project_handle. Auto-loads on miss.

func (c *ProjectCache) Load(ctx context.Context, idOrSlug string) (*LoadedProject, string, error)
// Same as Get, but explicitly used by the LoadProject RPC.

func (c *ProjectCache) MarkAccess(slug string)
// Bump LastAccessAt without a full Get.

func (c *ProjectCache) Evict(slug string)
// Drop from cache; idempotent.

func (c *ProjectCache) RunEvictor(ctx context.Context)
// Goroutine: every 60s, evict projects whose LastAccessAt + idleTTL < now,
// or LRU-evict beyond maxLoaded.
```

Loading walks the project's directory once: parses `project.yaml`, `summary.md`, every `tickets/<NNN>-…/ticket.yaml` + `body.md` + (if done) `completion.md`, every `comments/*.md`. Builds a fresh per-project `vecindex.Index` and runs the per-project sidecar backfill (T10).

### `Service` shape

```go
type Service struct {
    Store         *store.Store
    Embed         embed.Provider
    Cache         *cache.ProjectCache
    Worker        *worker.Worker
    LearningsIdx  *vecindex.Index
    SummaryIdx    *vecindex.Index
    Logger        *slog.Logger
    Cfg           config.Config
}

func New(cfg config.Config) (*Service, error)
```

T05/T06/T07/T16 all hang off the same `Service` and use `s.Cache.Get(ctx, slug)` whenever they need a loaded project.

### Methods

- **`CreateProject(ctx, slug, name, description, summary)`** — validate `summary >= 200 chars` after trim, slug uniqueness (walk projects), then `StageOp` writing `project.yaml` + `summary.md`. Update `SummaryIdx` enqueue (T10 marker). Returns the new Project.
- **`GetProject(ctx, idOrSlug)`** — `Cache.Get`, return `Project` from `LoadedProject.Project`.
- **`ListProjects(ctx)`** — `WalkProjects` returning lightweight `Project` summaries (loaded or not). Don't trigger lazy-load just for listing.
- **`UpdateProject(ctx, idOrSlug, in UpdateProjectInput)`** — load via cache, mutate `LoadedProject.Project` under its write lock, `StageOp` rewrite of `project.yaml` and (if summary changed) `summary.md`. If summary changed, enqueue `JobProjectSummary` and update `SummaryIdx`.
- **`DeleteProject(ctx, idOrSlug)`** — refuse if project has any non-`done` tickets (`ErrFailedPrecondition`). Otherwise: cache evict, then `os.RemoveAll(projects/<slug>/)`. Auto-commit captures the deletion.
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
- [ ] With `MAX_LOADED_PROJECTS=2`, loading a third project evicts the LRU.
- [ ] `ListProjects` does NOT trigger lazy loads (verify by checking cache size before/after).

## Notes

See **Project loading & in-memory cache**, **Project summary**, and **Service API > Projects** in [`../SPEC.md`](../SPEC.md). T05/T06/T07/T16 all reach into the project cache for their reads/writes — keep `LoadedProject.Lock` exposed and document the lock-ordering convention (always project lock, then store StageOp).
