# tickets_please — Design Spec

## Context

A Trello-like ticketing system whose primary user is an LLM, not a human. There's no frontend (yet) — just a Go binary that runs as an MCP server (stdio), spawned by any MCP-capable LLM client. All logic is in-process; no separate service to run.

Two design choices distinguish this from "Trello in Go":

1. **Every column move requires a comment.** No silent moves. The reason for the move becomes part of the audit trail.
2. **Completion is sacred and structured.** A ticket reaches `done` only via a dedicated `CompleteTicket` RPC that requires three fields: testing evidence, work summary, and learnings. The learnings get embedded and become semantically searchable, so future tickets can surface "have I hit this gotcha before?"

The system feeds itself: each completed ticket leaves machine-readable wisdom for the next agent. That's the actual point.

## Tech stack

| Concern | Choice |
|---|---|
| Language | Go |
| Transport / process model | **Single binary, MCP stdio default** (one binary, subcommand-dispatched). No gRPC, no protobuf. |
| Storage | **Filesystem** — a plain directory tree under `./.tickets_please/`, fully human-readable, committed to git |
| Data formats | YAML for structured records, Markdown for prose, JSON for embedding sidecar files |
| Vector search | In-memory brute-force cosine over loaded embedding sidecars; pluggable for HNSW later |
| Atomicity | Stage to `.tickets_please/.staging/<op-id>/`, then rename into place |
| Concurrency | Per-project `flock` for mutations + `fsnotify` cache invalidation across processes |
| Audit trail | Git commits per mutation (opt-out), authored as the calling agent |
| MCP SDK | `github.com/mark3labs/mcp-go` (stdio + streamable-HTTP transports) |
| File watching | `github.com/fsnotify/fsnotify` |
| File locking | build-tagged advisory locks: `golang.org/x/sys/unix` flock on Unix, `LockFileEx` (`golang.org/x/sys/windows`) on Windows |
| Embeddings | Ollama + `bge-m3` (1024-dim, 8192-ctx, local, default) — per-project, pluggable for OpenAI |
| Logging | `log/slog` |
| Config | `github.com/knadh/koanf/v2` — YAML file at `~/.tickets_please/config.yaml`, env-var overrides |
| YAML codec | `gopkg.in/yaml.v3` |
| UUIDs | `github.com/google/uuid` |
| Git ops | `github.com/go-git/go-git/v5` (pure-Go; no shell-out for the audit trail) |
| Tests | Temp dir per package — `t.TempDir()` — no Docker, no DB containers |

### Architecture: one binary, MCP-first

The system is **not** a long-running service. It's a single binary that runs in MCP-stdio mode whenever the LLM client spawns it, with all logic in-process. There's no gRPC server, no port to manage, no second binary.

Subcommand dispatch on `cmd/tickets_please/main.go`:
- `tickets_please mcp` *(default)* — stdio MCP server. The main mode.
- `tickets_please serve` — long-running HTTP MCP server (StreamableHTTP transport) for centralised mode. See "Centralised mode" below.
- `tickets_please init` — create `.tickets_please/` skeleton.
- `tickets_please migrate <repo-path> [--dry-run] [--data-root <path>]` — flatten a v0.1 `projects/<slug>/` layout to the v0.2 root shape and hoist legacy per-repo `agents/*.yaml` into the central `data_root`.
- `tickets_please check` — *(stub)* logs "not implemented yet"; the integrity walk currently runs only on startup (see **Integrity check**), not as a standalone command.
- `tickets_please help` (`-h`/`--help`) — usage.

### Centralised mode

`tickets_please serve` runs the same `svc.Service` + tool surface as `mcp`, but exposes it over HTTP via mcp-go's `StreamableHTTPServer`. One process can serve multiple repos without each one spawning its own stdio binary.

```
tickets_please serve [--addr :8765] [--data-root <path>] [--dev]
```

Mounts:
- `/mcp` — the StreamableHTTP MCP endpoint (per-connection sessions handled natively by mcp-go).
- `/healthz` — `200 ok` plaintext liveness probe.
- `/` and `/static/` — server-rendered web UI (`internal/web`). `html/template` + htmx, single binary, no separate process.

The web UI shares the same `svc.Service` instance as `/mcp`, so a human action in the browser and an LLM tool call from MCP write through the same concurrency-safe path. Cookie-scoped synthetic agents (`Web UI · <suffix>`) carry the audit trail. `--dev` swaps the embedded template/static FS for `os.DirFS` so edits show up on refresh without rebuilding.

A single `serve` process can host many repos. Each repo is registered as a *mount* — the server reads `<repo>/.tickets_please/project.yaml` and inserts a slug-keyed entry in its in-memory mount registry (`Service.projectMounts`). The list of mounted absolute paths is persisted to `<data_root>/registry.yaml` so the sidebar survives a restart. Mounts are added two ways: an HTTP client calls `register_agent` with `project_path`, or the web UI's `/p/load` form points at a repo on disk.

Wire it up in Claude Code:

```
claude mcp add --transport http tickets_please http://localhost:8765/mcp
```

HTTP clients **must** call `register_agent` once on connection to bind their session to a repo (its `project_path`); subsequent tool calls then accept `project_id_or_slug` as optional and fall back to the bound slug. Stdio clients pre-register at startup against `cfg.DataDir` and can skip the call.

Localhost only; no TLS, no auth — out of scope for v1.

**Why one binary, not a service:**
- An LLM-driven ticket tracker doesn't need a long-running process. The MCP lifecycle (one process per LLM client invocation) is the correct lifecycle.
- No port management, no IPC, no transport.
- When a frontend eventually wants the data, the same binary grows a `serve` subcommand that exposes the same `svc.Service` methods over HTTP/gRPC. Zero rework of the core.

**Layered architecture:**

```
+-----------------------------------------+
|  internal/mcptools  (mark3labs/mcp-go)  |   ← MCP tool wrappers, JSON ↔ Go types
+-----------------------------------------+
|  internal/svc       (Service struct)    |   ← all business logic, pure Go API
+-----------------------------------------+
|  internal/cache  internal/vecindex      |
|  internal/store  internal/embed         |   ← infrastructure
|  internal/agents internal/worker        |
+-----------------------------------------+
|  internal/config / internal/domain      |   ← configuration + plain types
+-----------------------------------------+
                 (filesystem)
```

`svc.Service` is the in-process API. MCP tools call it directly. Future HTTP/gRPC handlers will call the exact same methods.

### Why filesystem

- **Everything is in the open.** Every project, ticket, comment, and completion lives as a file you can `cat`, `grep`, and `git diff`. No opaque DB blob.
- **Git is the audit log.** Each mutation is a commit (configurable). `git log` shows who did what when. `git blame` works on every comment.
- **Hand-editable.** An LLM (or a human) can patch a `ticket.yaml` directly. The server reconciles on next read.
- **No external deps.** No SQLite extension to vendor, no Postgres to run, no migrations to manage. Clone the repo and you have data.
- **Branches are scratch experiments.** `git checkout -b` to try a workflow on isolated tickets. Merge or throw away.
- **Distribution is `git clone`.** That's the install instructions.

## Concurrent access

Multiple MCP processes can run simultaneously against the same `.tickets_please/`. Common case: Claude Desktop and Claude Code both spawn an MCP, both pointed at the same repo. The system handles this with three coordinated layers, none of which serialize unrelated work.

### Layer 1 — Atomic writes (already established)

Every mutation goes through `StageOp` (write to `.staging/<op-id>/`, then rename). Readers always see the old or the new file — never a partial write. Crash-mid-rename leaves residue that the integrity check finds.

### Layer 2 — Per-project `flock` for mutations

Mutations acquire an OS-level exclusive lock on the data dir's lock file before writing:

- **Project-scoped mutation** (CreateTicket, MoveTicket, CreateComment, etc.) → `flock(<data_dir>/.lock, LOCK_EX)` for the duration of `StageOp.Commit`. Post-flatten there's one project per data dir, so the per-project lock resolves to the data dir root (`Store.projectDir` ignores the slug and returns `Store.Root`).
- **Cross-data-root mutation** (anything touching the central agent registry under `<data_root>/`) → `flock(<data_root>/.lock, LOCK_EX)` briefly via the central `AgentStore`.
- **Reads do not lock.** They rely on atomic-write semantics for consistency.

Implementation: thin wrapper `internal/store/lock.go` dispatching to build-tagged backends — `lock_unix.go` (`golang.org/x/sys/unix.Flock`) and `lock_windows.go` (`golang.org/x/sys/windows.LockFileEx`), both polling at 50ms until the timeout. Locks release automatically on file close (or process death — the kernel reclaims). Acquisition has a configurable timeout (default 10s) to surface deadlocks rather than hang.

Two MCPs against **different data dirs**: zero contention. Two MCPs racing on the **same data dir**: serialized at the lock; correct semantics. Implemented on both POSIX (Linux/macOS) and Windows.

### Layer 3 — `fsnotify` cache invalidation

Cross-process consistency without polling. When a process loads a project into its in-memory cache, it sets up an `fsnotify` watcher on the project's data dir (`.tickets_please/`). Any change (from any process) emits an event; the cache:

1. Marks the project as "stale" (a flag, not an immediate evict).
2. The next call into that project reloads from disk.
3. If a write was in flight from this process, the watcher correlates and skips the invalidation (compare to a recent op-id).

Latency is ~10ms in practice on Linux. This is what actually makes multi-process coexistence safe — flock alone wouldn't catch a stale cache.

The `LoadedProject` struct gains a `Stale atomic.Bool` field; reads check it before serving.

### What this does NOT solve

- **Cross-machine sync.** Syncing `.tickets_please/` via Dropbox/Syncthing while two machines are simultaneously writing is out of scope. That's an OS-level FS coherence problem, not a data-store problem.
- **A misbehaving process holding the project lock indefinitely.** Surfaces as a "lock acquisition timed out after 10s" log line; the user can `kill` the bad process. Kernel releases the lock on death.

Windows is **not** a gap: `lock_windows.go` implements the same acquire/release contract via `LockFileEx`, and `build-windows` is a first-class Makefile target.

### Configuration

| Key | Env var | Default | Notes |
|---|---|---|---|
| `lock_timeout_seconds` | `LOCK_TIMEOUT_SECONDS` | `10` | How long to wait for a project/global lock before erroring. |
| `fsnotify_enabled` | `FSNOTIFY_ENABLED` | `true` | Set false to fall back to mtime polling on every read (slower, simpler). |

## Design decisions

- **Embedding default**: Ollama + `bge-m3` (1024-dim, 8192-token context). Embedders are **per-project** — each `project.yaml` declares its own `embed_provider`/`embed_model`, falling back to the server default; a mount probes its provider at attach and sizes its vec indexes to the probed dim. Provider is an interface; OpenAI impl ships alongside, switchable via the `embed_provider` config key. A missing Ollama model is acquired in the background (the boot/attach path never blocks on a pull) and the project re-embeds when it lands; a sidecar whose stamped provider/model/dim no longer matches its mount is treated as stale and rebuilt.
- **Comments are immutable.** No `UpdateComment` or `DeleteComment` RPCs. Typos get a follow-up comment. Audit trail stays clean.
- **No reopen.** Once `CompleteTicket` runs, the ticket is frozen in `done`. If work resurfaces, create a new ticket — past learnings still surface via `SearchLearnings`. Keeps the completion contract meaningful.
- **Every mutation is attributed.** Agents introduce themselves before doing anything that changes state. Identity is self-asserted (no auth, this is a hobby project — the trust model is "we trust agents to be honest about who they are"); the value is observability, not access control.

## Agent identity & sessions

The system is multi-agent by design. Every state-changing action records *which agent did it, when*. Reads are unattributed (they don't change anything).

### Lifecycle

1. **Register.** An agent calls `AgentService.RegisterAgent` with a self-chosen `key`, a display `name`, and an arbitrary `metadata` map. The server creates an `agents` row with a server-generated UUID, stores the agent's fields, and returns `{session_id, expires_at}`.
2. **Use.** On every mutating call, the MCP tool layer puts the session UUID into the call's `context.Context`. An in-process middleware validates it (exists, not expired) and attaches `*domain.Agent` for handlers to read.
3. **Heartbeat (optional).** `AgentService.Heartbeat(session_id)` updates `last_seen_at` without extending the TTL. Useful for activity dashboards; never required for normal operation.
4. **Expire.** Sessions have a fixed TTL (default 60 minutes, configurable via `agent_session_ttl_minutes`). Expired sessions are rejected with `Unauthenticated`. Agents re-register; they may reuse the same `key` if it's no longer active.

The `key` is **agent-generated** — it's the agent's claim about who they are (e.g. `claude-sonnet-4-6:run-2026-05-02T13:30:00Z-abc123`). The server doesn't authenticate it; it just records it. This is intentional: the audit trail captures intent, not identity-proven-by-cryptography.

### Which methods require a session?

| Category | Examples | Session required? |
|---|---|---|
| Mutating | Create/Update/Delete/Move/Complete/Assign on Project, Phase, Ticket, Comment | **Yes** — middleware enforces |
| Read-only | Get/List/Search | No |
| Agent management | Register/Heartbeat/GetAgent | No (Register is the entry point; Heartbeat self-identifies in the request) |

### Storage shape for identity

Each active or expired session is a yaml file at `.tickets_please/agents/<session-uuid>.yaml`. The full file format is in **Data layout** above; the record struct (`store.AgentRecord`) lives in `internal/store/`.

Attribution refs on other entities are nullable string fields in their yaml:

- `project.yaml` → `created_by: <agent-uuid>` (or `null`)
- `tickets/<NNN>-…/ticket.yaml` → `created_by`, `completed_by` (each nullable), plus `created_for`, `completed_for` (`<user-uuid>`, set when the authoring agent was acting for a registered user)
- `phases/<NNN>-…/phase.yaml` → `created_by` (nullable)
- comment frontmatter → `author_id: <agent-uuid>` (or `null`), plus `author_for: <user-uuid>` when acting-for

An agent may bind its session to a registered user via `register_agent`'s `acting_for_user_id`. When bound, the agent inherits that user's per-project membership (it may only mutate projects the user can access; viewers are read-only — failures return `forbidden`), and the `*_for` fields above record the human on whose behalf each write happened. Plain key-only agents leave all `*_for` fields `null` and are unrestricted — the default.

These ref fields are nullable so projects/tickets/comments can be created or hand-edited before T15's middleware is enforcing. Once T15 lands and the middleware runs, every newly-created row populates its attribution. Pre-existing entities keep `null` — no backfill, since there's no identity to backfill *to*. The integrity check (T02) warns on dangling refs (an `agent_id` that doesn't resolve to a file in `agents/`) but doesn't fail boot.

### Identity attached to context

Mutating service methods read the calling agent from `ctx`:

```go
func WithAgent(ctx context.Context, a *domain.Agent) context.Context
func AgentFrom(ctx context.Context) (*domain.Agent, bool)
```

A small middleware (`internal/svc/middleware.go`) wraps every mutating method and:
1. Reads `agent_session_id` from a context value the MCP tool layer set (the MCP server registered itself on startup and threads its own session ID into every tool call's context).
2. Loads the agent from `Store.GetAgent`.
3. If missing or expired → returns `ErrUnauthenticated`.
4. Attaches `*domain.Agent` to the context via `WithAgent`.
5. Calls the underlying method.

Reads (`Get*`, `List*`, `Search*`) skip the middleware.

The MCP layer is responsible for putting `agent_session_id` into the context. In MCP-stdio mode the server registers itself once on startup and uses that ID for every tool call. In a future `serve` mode, an HTTP/gRPC interceptor would parse it from a header instead — same plumbing, different transport.

### Surfacing attribution on reads

The `Ticket` and `Comment` messages gain optional `AgentRef` fields:

- `Ticket.created_by` (AgentRef, optional)
- `Ticket.completed_by` (AgentRef, optional, set only when `column == DONE`)
- `Comment.author` (AgentRef, optional)

Implementations join `agents` on the relevant id columns when reading. `AgentRef` is a flat summary (id + name) so reads don't drag the full metadata blob unless the caller explicitly fetches it via `GetAgent`.

### MCP integration

The `tickets_please mcp` binary, on startup:
1. Resolves its own identity (defaults to `tickets_please_mcp:<random-8-hex>` or values from `MCP_AGENT_KEY` / `MCP_AGENT_NAME`).
2. Calls `Service.RegisterAgent` once (in-process call) and caches the returned session ID.
3. Each MCP tool handler attaches that session ID to its `context.Context` before calling into `svc.Service`.

If a session expires mid-conversation, the MCP layer auto-re-registers and retries — the LLM never sees the failure. No transport metadata, no headers. Just context-passing inside the same process.

*(Configuration keys for agent sessions are listed in the **Configuration** table above.)*

## Configuration

Config loads in order (later sources override earlier):

1. **Built-in defaults** (sensible local-dev values).
2. **YAML file** at `~/.tickets_please/config.yaml` (if present).
3. **Environment variables** (uppercased keys with underscores).

The directory `~/.tickets_please/` is created on first run if missing. A sample config is shipped in the repo at `examples/config.yaml`.

### Config keys

| Key | Env var | Default | Notes |
|---|---|---|---|
| `data_dir` | `DATA_DIR` | `./.tickets_please` | Per-repo project data dir (one project's `project.yaml`, `phases/`, `tickets/` live here). Defaults to a directory in the cwd. |
| `data_root` | `DATA_ROOT` | `~/.tickets_please` | Central data root shared across all repos this server hosts. Holds the agent registry (`agents/<uuid>.yaml`) and the mount registry (`registry.yaml`). Tilde-expanded at load time. |
| `remote_project_root` | `REMOTE_PROJECT_ROOT` | `~/.tickets_please/projects` | Bounds where `create_project` may materialise a new project dir when the caller's `project_path` doesn't yet exist on the server. A missing path outside this root is rejected; existing paths are used as-is. Empty disables auto-create (strict stdio semantics). |
| `auto_commit` | `AUTO_COMMIT` | `true` | If true and `data_dir` is inside a git repo, every mutation produces a commit authored as the calling agent. |
| `embed_provider` | `EMBED_PROVIDER` | `ollama` | `ollama` or `openai`. |
| `ollama_url` | `OLLAMA_URL` | `http://localhost:11434` | Used when `embed_provider=ollama`. |
| `ollama_model` | `OLLAMA_MODEL` | `bge-m3` | Server-default model used when `embed_provider=ollama` and a project doesn't override it. |
| `openai_api_key` | `OPENAI_API_KEY` | *(empty)* | Required when `embed_provider=openai`. |
| `mcp_agent_key` | `MCP_AGENT_KEY` | *(generated)* | Self-asserted identity for the MCP process; defaults to `tickets_please_mcp:<random>`. |
| `mcp_agent_name` | `MCP_AGENT_NAME` | `tickets_please_mcp` | Display name the MCP registers as. |
| `agent_session_ttl_minutes` | `AGENT_SESSION_TTL_MINUTES` | `60` | Default session TTL. Server caps `requested_ttl` at this value. |
| `agent_session_max_minutes` | `AGENT_SESSION_MAX_MINUTES` | `240` | Hard upper bound; even an explicit `requested_ttl` longer than this is clamped. |
| `project_idle_minutes` | `PROJECT_IDLE_MINUTES` | `15` | Sliding idle TTL before a loaded project is evicted from the in-memory cache. |
| `max_loaded_projects` | `MAX_LOADED_PROJECTS` | `16` | Cap on simultaneously-loaded projects. LRU eviction beyond this. |
| `lock_timeout_seconds` | `LOCK_TIMEOUT_SECONDS` | `10` | Per-project flock acquisition timeout for mutations. |
| `fsnotify_enabled` | `FSNOTIFY_ENABLED` | `true` | Cross-process cache invalidation via fsnotify. Set false for mtime polling fallback. |
| `enforce_dependencies` | `ENFORCE_DEPENDENCIES` | `false` | When true, `MoveTicket` to `in_progress` blocks if `BlockedBy` is non-empty. False (the default) only logs a warning and annotates the move comment. |

## Project layout

```
tickets_please/
├── go.mod
├── Makefile                       # build, build-windows, run, test, init-config, init-data, check
├── SPEC.md                        # this file
├── examples/config.yaml           # sample config users copy to ~/.tickets_please/
├── .tickets_please/               # this repo's own data dir (committed)
│   ├── README.md                  # explains the layout to anyone clicking around
│   ├── project.yaml               # the project record
│   ├── summary.md                 # required ≥200-char context doc
│   ├── phases/                    # optional sub-projects
│   ├── tickets/                   # phase-less tickets
│   └── .staging/                  # transient atomicity scratch (gitignored)
├── internal/
│   ├── config/                    # koanf-based loader
│   ├── domain/                    # plain Go types: Project, Phase, Ticket, Comment, Agent, Column...
│   ├── store/                     # filesystem storage primitives
│   │   ├── store.go               # Store struct: paths, atomic writes, reads
│   │   ├── stage.go               # staging-dir + rename atomicity helper
│   │   ├── lock.go                # per-project flock helpers
│   │   ├── watch.go               # fsnotify wrappers
│   │   ├── git.go                 # auto-commit hook (go-git)
│   │   ├── projects.go            # project read/write/walk
│   │   ├── phases.go
│   │   ├── tickets.go
│   │   ├── comments.go
│   │   ├── agents.go              # central AgentStore (lives at <data_root>/agents/)
│   │   └── integrity.go           # startup integrity check
│   ├── cache/                     # project cache with sliding TTL eviction
│   ├── vecindex/                  # in-memory vector index (cosine, brute-force)
│   ├── embed/                     # provider interface + impls
│   │   ├── provider.go
│   │   ├── ollama.go
│   │   └── openai.go
│   ├── worker/                    # async embedding worker (per mount)
│   ├── log/                       # in-memory ring-buffer log handler (web UI log view)
│   ├── svc/                       # business logic — methods on Service{}
│   │   ├── service.go             # Service struct, New(), per-mount registry
│   │   ├── registry.go            # persistent mount registry (registry.yaml)
│   │   ├── middleware.go          # session-validating in-process middleware
│   │   ├── hydrate.go             # assembles store records + sibling files into domain types
│   │   ├── projects.go            # CreateProject + CreateProjectAt
│   │   ├── phases.go
│   │   ├── waves.go               # ListWaves
│   │   ├── tickets.go
│   │   ├── tickets_phase.go       # AssignTicketToPhase
│   │   ├── comments.go
│   │   ├── search.go
│   │   ├── agents.go
│   │   └── validation.go
│   ├── mcptools/                  # mark3labs/mcp-go tool wrappers around svc
│   │   ├── tools.go               # all 35 tool registrations + handlers
│   │   ├── format.go              # domain → LLM-friendly JSON
│   │   ├── instructions.go        # the cross-tool workflow reflexes string
│   │   └── identity.go            # stdio self-registration helpers
│   └── web/                       # html/template + htmx web UI mounted by `serve`
└── cmd/
    └── tickets_please/main.go     # one binary; CLI dispatches subcommands (mcp default)
```

Go module name: `tickets_please`. **One binary**, subcommand-dispatched. No `proto/`, no `gen/`, no `buf*` configs, no separate gRPC server.

There are two on-disk roots:

- **Per-repo `data_dir` (`<repo>/.tickets_please/`)** — a single project lives at the data-dir root (post-flatten). Committed to git so the project's history travels with the repo.
- **Central `data_root` (default `~/.tickets_please/`)** — agent sessions (`agents/<uuid>.yaml`) and the persistent mount registry (`registry.yaml`). Shared across every repo this server instance hosts.

A repo's data dir starts out with just `.staging/` and the `README.md`; the project record is written by `create_project` (or its web equivalent), not by `init`.

## Phases (optional sub-projects)

For bigger bodies of work, a project can be subdivided into **phases** — lightweight containers that group tickets into chunks of work without imposing their own lifecycle. Phases are optional: small projects don't need them and put tickets directly in the project; larger projects organize.

### Shape

A phase carries the same kind of context that makes projects useful, scaled down:

| Field | Required? | Notes |
|---|---|---|
| `id` (uuid) | yes | server-generated |
| `project_id` | yes | parent project |
| `slug` | yes | unique within the project |
| `number` | yes | per-project sequence; encodes order |
| `name` | yes | display label |
| `description` | no | one-liner |
| `summary` | **yes (≥200 chars)** | markdown doc — same load-bearing role as project summary, scoped to this phase |
| `created_by` | yes (post-T15) | agent attribution |
| `created_at`, `updated_at` | yes | timestamps |

### What a phase does NOT have

- **No column / state.** Phases are organizational, not lifecycle-managed. "Phase done" is implicit ("all my tickets are done"). No `CompletePhase` RPC, no required retrospective, no frozen state.
- **No nested phases.** Two levels is plenty (project → phase → ticket). Anything deeper should be a separate project linked from the parent's summary.

### Waves (soft grouping inside a phase or project)

A **wave** is a soft integer tag on a ticket that lets a planner cluster tickets into ordered batches without committing to hard `depends_on` edges. Waves are organizational, exactly like phases: no enforcement, no schema, no separate file.

- `Ticket.Wave int` (default `0` = "unassigned").
- Scope: a wave number is meaningful within whatever organizes the ticket — its phase if phased, its project if phase-less. Wave 1 of phase A is unrelated to wave 1 of phase B.
- Hard ordering still belongs in `depends_on`; waves are a hint for *grouping*, not a constraint for *gating*.

Use cases:
- A planning agent breaks a phase into "research wave 1", "build wave 2", "ship wave 3" without writing every dep edge between them.
- An orchestrator agent works wave-by-wave, fanning subagents across all tickets in the same wave.
- A human glances at `list_waves` to see how a phase decomposes.

`ListTickets` gains an optional `wave` filter. `list_waves` is an MCP tool that returns wave-level summaries (count, active count) for the chosen scope.

### Tickets and phases

`Ticket.phase_id` is optional. A ticket either belongs to a phase or sits directly under its project. `AssignTicketToPhase(ticket_id, phase_id?, comment)` moves tickets between phases (or to no phase) — requires a comment, mirroring `MoveTicket` semantics.

### File layout

Post-flatten (v0.2): one project per `.tickets_please/` data dir; project content
sits at the data-dir root rather than nested under `projects/<slug>/`. Agent
sessions moved out of the per-repo dir into the central `data_root` in v0.3 so a
single server can host multiple repos without each one duplicating the agent
registry.

```
<repo>/.tickets_please/          # per-repo (committed to git)
├── project.yaml
├── summary.md
├── summary.embedding.json
├── tickets/                     # phase-less tickets sit here
│   └── <NNN>-<slug>/
├── phases/                      # only present when the project has phases
│   └── <NNN>-<phase-slug>/
│       ├── phase.yaml
│       ├── summary.md
│       ├── summary.embedding.json
│       └── tickets/
│           └── <NNN>-<ticket-slug>/
├── .lock                        # per-data-dir flock (gitignored)
└── .staging/                    # transient atomicity scratch dir (gitignored)

~/.tickets_please/                # central data_root (NOT in a repo)
├── agents/<session-uuid>.yaml   # one file per agent session, active or expired
├── registry.yaml                # persisted absolute paths the server has mounted
├── config.yaml                  # optional user-level config
└── .staging/                    # transient atomicity scratch for agents/ writes
```

Repos still on the v0.1 `projects/<slug>/` shape can be flattened with
`tickets_please migrate <repo-path> [--dry-run] [--data-root <path>]`. The
migrate command also hoists any per-repo `agents/*.yaml` from the legacy
location into `<data_root>/agents/`.

Ticket `number` is **project-level** — i.e. one global sequence across phased + phase-less tickets — so a ticket reference is stable as it shuffles between phases. The path locates a ticket by phase membership; the number identifies it across the project.

### Service API additions

(See **Service API > Phases** above for full method signatures.)

`Ticket.PhaseID` is `*string` — `nil` means phase-less.
`ListTicketsInput.PhaseIDOrSlug` is `*string`: `nil` = any phase or none; `*"-"` (sentinel) = phase-less only; `*"foo"` = that phase. The same convention applies anywhere a `phase_id_or_slug` parameter accepts the optional phase-less sentinel.

### MCP tool additions

| Tool | Description |
|---|---|
| `create_phase` | Add a phase to a project for bigger bodies of work. Requires a `summary` (≥200 chars) — same load-bearing context doc as projects, scoped to this phase. |
| `list_phases` | List phases in a project. Returns counts of total + active tickets per phase. |
| `get_phase_summary` | Fetch a phase's full summary markdown. Read this when entering a phase, the same way you'd read a project summary. |
| `update_phase` | Edit a phase's name/description/summary. |
| `assign_ticket_to_phase` | Move a ticket between phases (or to no phase). Requires a comment explaining why — same audit-trail rule as `move_ticket`. |
| `list_waves` | List the waves in a phase (or in the phase-less area of a project) with per-wave ticket counts. Use this to see how a body of work decomposes before picking what to start. |

The two summary-reading tool descriptions (`get_project_summary` and `get_phase_summary`) form a hierarchy the LLM should walk: read project summary → read phase summary (if applicable) → read ticket body.

## Project summary

A project is more than a name. Every project carries a **summary**: a required markdown document (min ~200 characters) that an LLM can context-load before doing any work in that project. The summary describes goals, constraints, key components, and anything else the planning agent thinks matters.

- Required at `CreateProject` time. Server rejects summaries shorter than 200 characters after trim.
- Stored as `summary.md` alongside `project.yaml` and embedded into a `summary.embedding.json` sidecar (bge-m3, 1024-dim by default) so related work is semantically discoverable. The embedding dim is whatever the project's configured provider returns — probed at attach, not hardcoded.
- Exposed on the `Project` message so it travels with every read.
- MCP has a dedicated `get_project_summary` tool whose description tells the LLM: *"Read this before starting work in a project — it's the project's design context."*
- Editable via `UpdateProject(summary?)` — re-embedding is triggered on change. Edits don't carry forward through git-style history; the latest summary wins. (Comments form the audit trail; the summary is intentional living documentation.)

## Data layout

Data is split between two roots: a per-repo `data_dir` (default `./.tickets_please/`, committed to git) holding one project's content, and a central `data_root` (default `~/.tickets_please/`, **not** in any repo) holding the agent registry and the persistent mount registry. Both trees are the source of truth; in-memory state is reconstructable from the files on disk.

```
<repo>/.tickets_please/                      # per-repo, one project per data dir
├── README.md                                # short orientation for anyone browsing the repo
├── project.yaml                             # id, slug, name, description, created_by, created_at, updated_at
├── summary.md                               # the required markdown summary (≥ 200 chars)
├── summary.embedding.json                   # embedding sidecar: {provider, model, dim, vec}
├── tickets/                                 # phase-less tickets sit here
│   └── <NNN>-<slugified-title>/
│       ├── ticket.yaml                      # id, title, column, body_path, created_by, completed_by, completed_at, created_at, updated_at
│       ├── body.md
│       ├── body.embedding.json
│       ├── completion.md                    # only when column == done
│       ├── learnings.embedding.json         # only when column == done
│       └── comments/
│           ├── <ts>-<short-id>-<kind>.md           # one file per comment
│           └── <ts>-<short-id>-<kind>.embedding.json
├── phases/                                  # only present when the project uses phases
│   └── <NNN>-<phase-slug>/
│       ├── phase.yaml
│       ├── summary.md
│       ├── summary.embedding.json
│       └── tickets/                         # phase-scoped tickets
│           └── <NNN>-<slugified-title>/...
└── .staging/                                # transient atomicity dir; emptied on graceful shutdown

~/.tickets_please/                            # central data_root
├── agents/
│   └── <session-uuid>.yaml                  # one file per agent session (active or expired)
├── registry.yaml                            # absolute paths the server has mounted (sidebar persistence)
├── config.yaml                              # optional user-level config
└── .staging/                                # transient atomicity scratch for agents/ writes
```

### File formats

**`project.yaml`** *(example)*:

```yaml
id: 7e2f4a4d-9c4b-4a1e-9b2f-2c5e9a3b6d11
slug: tickets_please
name: tickets_please
description: A Trello-like ticketing system designed for LLM agents.
embed_provider: ollama                              # per-project embedder (falls back to server default)
embed_model: bge-m3                                 # sidecar identity stamp; a mismatch triggers a rebuild
created_by: 8a51c2c0-22ad-4e7c-92d1-f9d6e7a17b50    # agent.id
created_at: 2026-05-02T13:42:11.123Z
updated_at: 2026-05-02T13:42:11.123Z
```

**`ticket.yaml`** *(example)*:

```yaml
id: c0a55d8c-3d63-4f6a-b3a7-9e8a1d8c2f44
project_id: 7e2f4a4d-9c4b-4a1e-9b2f-2c5e9a3b6d11   # the project UUID, not the slug
number: 7
title: Implement MoveTicket transactional flow
column: in_progress           # one of: todo, in_progress, testing, done
created_by: 8a51c2c0-22ad-4e7c-92d1-f9d6e7a17b50
completed_by: null
completed_at: null
created_at: 2026-05-02T13:50:01.000Z
updated_at: 2026-05-02T14:11:09.000Z
# phased tickets also carry:  phase_id: <uuid>  and  wave: <int>
# dependency fields when set:  depends_on: [<id>, …]   parallelizable_with: [<id>, …]
```

`body.md` is the ticket description. `completion.md` (only when `column: done`) is structured:

```markdown
## Testing evidence
<text>

## Work summary
<text>

## Learnings
<text>
```

**`agents/<session-uuid>.yaml`** *(example)*:

```yaml
id: 8a51c2c0-22ad-4e7c-92d1-f9d6e7a17b50
key: claude-sonnet-4-6:run-2026-05-02T13:30:00Z-abc123
name: Claude Sonnet 4.6
metadata:
  client: tickets_please_mcp
  host: dev-laptop
created_at: 2026-05-02T13:30:00.000Z
expires_at: 2026-05-02T14:30:00.000Z
last_seen_at: 2026-05-02T14:11:09.000Z
```

**Comment filename convention**: `<created_at_compact>-<short-id>-<kind>.md`, e.g. `20260502T141109Z-c0a5-system_move.md`. Sorting filenames alphabetically yields creation order. The companion `*.embedding.json` sits next to it.

Comment file content has a small frontmatter block followed by markdown body:

```markdown
---
id: 8d3a4f1e-2b6c-4d8e-9a2f-1c5e9a3b6d22
ticket_id: c0a55d8c-3d63-4f6a-b3a7-9e8a1d8c2f44
kind: system_move
author_id: 8a51c2c0-22ad-4e7c-92d1-f9d6e7a17b50
from_column: todo                                  # system_move only; absent on user/system_completion
to_column: in_progress                             # system_move only
created_at: 2026-05-02T14:11:09.000Z
---
Picked this up after read-through; starting on the validation layer first.
```

### Atomicity (the staging + rename pattern)

Multi-file mutations (e.g. `MoveTicket` updates `ticket.yaml` AND inserts a `system_move` comment; `AssignTicketToPhase` renames an entire ticket directory between `tickets/` and `phases/<NNN>-…/tickets/` AND updates `ticket.yaml`) follow an **ordered-operations** model — not a flat file map.

Each `StageOp` carries a list of ordered ops:

| Op | What it does | When prepared | When applied |
|---|---|---|---|
| `Write(relPath, content)` | Writes a file. | At Write time: stages the file under `.staging/<op-id>/<relPath>` (mkdir parents, `f.Sync()`). | At Commit time: `os.Rename` from staging → final. |
| `RenameDir(fromRel, toRel)` | Moves an entire directory in-place (within `data_dir`). | No-op at prepare time. | At Commit time: `os.Rename(fromAbs, toAbs)`. Single syscall, atomic per rename. |
| `RemovePath(relPath)` | Deletes a file or tree. | No-op at prepare time. | At Commit time: `os.RemoveAll`. |

`Commit` flow:

1. Acquire the appropriate flock (per-project or global — see **Concurrent access**).
2. **Prepare phase**: re-validate every staged Write file is on disk under `.staging/<op-id>/`.
3. **Apply phase**: iterate `ops` in declared order; each op is applied via a single syscall where possible. Failures abort and leave whatever has already been applied; integrity check at next startup detects.
4. `os.RemoveAll(.staging/<op-id>/)`.
5. Auto-commit (if enabled) captures the touched paths.
6. Release the lock.

Failure between prepare and apply leaves staging-dir residue but no on-disk damage.
Failure mid-apply leaves a partial state that the **integrity check** detects and surfaces. We deliberately don't claim multi-op atomicity beyond per-op syscall atomicity — instead, we make recovery legible.

For single-file writes (e.g. `CreateComment`), the StageOp has one Write op and the prepare/apply degenerates to a single staged-file rename.

### Disk records vs domain types

The on-disk yaml schema is **not** the same as the in-memory `domain.*` type. They serve different audiences:

| Layer | Type | What it carries |
|---|---|---|
| Disk | `store.ProjectRecord` | Just the fields stored in `project.yaml` — id, slug, name, description, attribution, timestamps. **No** `summary`/`body` fields (those are sibling files). |
| Disk | `store.TicketRecord` | id, project_id, title, column, attribution, deps/parallel/phase ref, timestamps. **No** `body`, `learnings`, etc. |
| Disk | `store.CommentRecord` | id, kind, author_id, from_column, to_column, created_at — i.e. the frontmatter. **No** body. |
| Disk | `store.AgentRecord` | All Agent fields (it's a flat yaml, no sidecar). |
| Disk | `store.PhaseRecord` | id, project_id, slug, number, name, description, attribution, timestamps. **No** `summary`. |

Hydrated `domain.*` types (T03) carry the full record **plus** the markdown bodies (`Summary`, `Body`, `Learnings`, etc.) loaded from sibling files. Store-level reads return records; the cache layer assembles records + sibling files into domain types.

This separation keeps yaml frontmatter small and inspectable while domain types travel with their context-loaded prose.

### Integrity check (startup)

On startup the server walks `.tickets_please/` and:
- Validates every `*.yaml` parses.
- Confirms every referenced agent UUID exists (or is null).
- Confirms every ticket directory has `ticket.yaml` and `body.md`.
- Confirms `done` tickets have `completion.md`.
- Confirms each ticket's `column` matches its placement (no `done` ticket missing its completion file).
- Confirms `.staging/` is empty (else logs the partial op-id and instructions to inspect).

Any failure surfaces as a clear log line. Fatal failures abort startup; soft inconsistencies (orphan embeddings) are logged but don't block boot.

### Auto-commit

When `auto_commit: true` and `data_dir` lives inside a git repo, every mutation produces a single commit:

```
[tickets_please] <verb> <subject> [<agent-name>]

<short body summarizing the change>
```

Commit author: `<agent.name> <agent.key>`. Timestamps come from `agent.created_at` so commit history aligns with action time.

If `data_dir` is **not** in a git repo, auto-commit is silently disabled (warn-log once at startup). Users can opt out with `auto_commit: false` even in a git repo.

### Why this layout

- **Agents have their own dir** so attribution lookups are O(1) by uuid (`agents/<uuid>.yaml`). Active-session uniqueness checked by walking the dir on register — fine for tens of agents.
- **Projects keyed by slug** in path so `find` and `ls` give immediately-readable structure. The canonical id is still in `project.yaml`.
- **Ticket dirs include the number prefix** (`007-…`) so listings sort to creation order and the path itself tells you the ticket id at a glance.
- **Comments are timestamp-prefixed individual files** so `ls` orders them chronologically and each comment is independently grep-able.
- **Embeddings are JSON sidecar files** so they're inspectable, diffable, and (importantly) trivially regenerable from their source — `find -name '*.embedding.json' -delete` plus a worker restart re-embeds everything.
- **Completion fields live in `completion.md`** as headed sections, single source of truth, exactly what `list_comments` will surface as the auto-generated `system_completion` comment.

## Project loading & in-memory cache

Walking the tree and parsing yamls on every call would be wasteful when an agent does dozens of ops on the same project. The cache keeps each loaded project warm with a sliding idle TTL.

### Behavior — lazy + optional

- **Lazy auto-load.** Any project op (`GetProject`, `CreateTicket`, `MoveTicket`, …) that references a project the server hasn't loaded triggers a transparent load. The agent never has to think about it.
- **Optional explicit load.** `ProjectService.LoadProject(slug)` warms the cache eagerly and returns a `project_handle` — useful when an agent is about to do many ops and wants to pre-pay the load cost.
- **Sliding TTL.** Each call against a loaded project bumps `project.last_access_at`. When `(now - last_access_at) > project_idle_minutes`, the eviction loop removes it from memory.
- **Memory cap.** `max_loaded_projects` (default 16) bounds memory. When the cap is hit, eviction picks the LRU project, regardless of its TTL.

### What the loaded state contains

```go
type LoadedProject struct {
    Project       *domain.Project              // parsed project.yaml + summary.md
    Phases        map[string]*domain.Phase     // id → phase
    PhasesBySlug  map[string]*domain.Phase     // slug → phase
    Tickets       map[string]*domain.Ticket    // ticket id → ticket (yaml + body.md + completion.md if done)
    Comments      map[string][]*domain.Comment // ticket id → ordered comment list
    LoadedAt      time.Time
    LastAccessAt  time.Time
    Stale         atomic.Bool                  // flipped by fsnotify when files change cross-process
    Lock          sync.RWMutex
    // (plus unexported fsnotify watcher + stopWatch channel, closed on eviction)
}
```

Vector indexes do **not** live on `LoadedProject` — they're owned by the `ProjectMount` (four indexes + a worker per mount; see **Vector search index**), so a mount's vectors are freed when it's evicted. There is no always-resident global index; the only resident structure is `Service.defaultIndexes`, the empty-registry fallback.

### `LoadProject` method

```go
type LoadProjectResult struct {
    Project           *domain.Project
    Handle            string         // opaque, returned for diagnostics; not used to identify project
    ExpiresAt         time.Time      // = LastAccessAt + idle TTL
    TicketCount       int
    ActiveTicketCount int
}

func (s *Service) LoadProject(ctx context.Context, idOrSlug string) (LoadProjectResult, error)
```

Returns the loaded project plus a diagnostic handle. **The handle is not used to identify subsequent calls** — every call just passes the slug or id. The handle is exposed so the MCP `load_project` tool can return something useful for an LLM that's introspecting cache state (the `who_am_i` / `loaded_projects` tools). Internally the cache is keyed on slug.

Eviction is invisible to callers — the next call lazy-reloads the project. There is no "stale handle" error.

### Configuration

| Key | Env var | Default | Notes |
|---|---|---|---|
| `project_idle_minutes` | `PROJECT_IDLE_MINUTES` | `15` | Idle TTL before eviction. |
| `max_loaded_projects` | `MAX_LOADED_PROJECTS` | `16` | Memory cap (LRU beyond this). |

### Eviction

A single goroutine wakes every 60s and evicts projects whose `last_access_at + project_idle_minutes < now`. Eviction:
1. Acquires the project's write lock (so no in-flight calls are interrupted).
2. Drops the `LoadedProject` from the map, including its vector index slice.
3. Logs at info: `evicted project <slug> after <duration> idle`.

Evicted projects are not deleted from disk — only from memory. Any future op on them re-loads transparently.

### Why this beats a JWT-style token model

- **No new token to manage.** The agent's existing `x-agent-session` is enough auth (such as it is). The project state is just a server-side optimization.
- **Lazy = the LLM never sees plumbing.** It calls `MoveTicket` with a slug; loading is invisible.
- **Optional explicit load = power-users get a perf hint.** Agents about to do batch work pre-warm.
- **Eviction is a server detail.** No agent-facing error states for "project handle expired."

## Ticket dependencies & subagent orchestration

Tickets can declare two relationships:

- **`depends_on: [ticket_id, …]`** — hard prerequisite. A ticket's deps must all be `done` before the ticket can be moved past `todo`. Default v1 enforcement: **soft** (server warns and includes a `BlockedBy` field on the ticket; `MoveTicket` returns `FailedPrecondition` when `enforce_dependencies: true`).
- **`parallelizable_with: [ticket_id, …]`** — purely advisory. Hints that two tickets can be worked simultaneously. Surfaced in `Ticket` reads so subagent orchestrators know what fans out cleanly.

Why both fields:
- An orchestrator agent walks `ListTickets(column=todo, ready_only=true)` and gets a queue of unblocked tickets. It can then spawn subagents for everything in `parallelizable_with` lockstep.
- Hard deps prevent agents from racing each other on tickets where order matters (e.g. "T05 needs T02 + T03 done first").

`Ticket` carries:
- `DependsOn []string` — ticket ids; hard prerequisite for moving past `todo`.
- `ParallelizableWith []string` — ticket ids; advisory.
- `BlockedBy []string` — computed at read: subset of `DependsOn` not yet `done`.

Server adds:
- `ListTickets(..., ready_only)` — when true, filters to tickets with empty `BlockedBy` and column ∈ {todo, in_progress}. Idea-kind tickets are never "ready work" — they're excluded even if `include_ideas` is also set.
- `enforce_dependencies` config key (default `false` for v1 — soft warnings; `true` blocks `MoveTicket` when `BlockedBy` is non-empty).

The same concept is used in this very repo's `planning/` directory (the planning tickets ARE a dependency-graphed work queue). See **Planning directory subagent schema** below for the frontmatter format.

### Planning directory subagent schema

`planning/T*.md` files use YAML frontmatter so subagents can parse status and dependencies without scraping markdown:

```yaml
---
id: T04
title: ProjectService + project cache
status: TODO                              # TODO | IN_PROGRESS | DONE | BLOCKED
owner: ""                                 # agent name when claimed; empty otherwise
depends_on: [T02, T03, T15]
parallelizable_with: [T05, T06, T08]
wave: 2                                   # 0=must-be-first; later waves depend on earlier
files:
  - internal/svc/service.go
  - internal/svc/projects.go
  - internal/cache/projectcache.go
estimate: medium                          # tiny | small | medium | large
stretch: false
---
```

This is exactly the shape that the runtime `Ticket` type carries — it's the same concept, dogfooded.

## Service API (Go, in-process)

`internal/svc.Service` is the single in-process API. MCP tools call it directly. A future `serve` subcommand will expose the same methods over HTTP/gRPC; that's transport, not surface.

Every mutating method requires an agent identity attached to its `context.Context` (see **Agent identity & sessions**). Reads do not.

```go
type Service struct {
    Store      *store.Store        // "default" store for stdio mode; the mount registry is canonical in HTTP mode
    AgentStore *store.AgentStore   // central agent-session store under <data_root>/agents/
    Cache      *cache.ProjectCache
    Embed      embed.Provider      // server-default provider; per-mount providers shadow it
    EmbedDim   int                 // dim of the server-default Embed
    EmbedNew   func(embed.EmbedConfig) (embed.Provider, error)  // per-mount provider factory (tests inject fakes)
    Logger     *slog.Logger
    Cfg        config.Config
    // defaultIndexes — resident fallback consulted only when the mount registry is empty.
    // projectMounts (guarded by mountsMu) is the per-project registry; each mount owns its
    // own Store, embed provider, four vec indexes, and embedding worker.
    // (plus unexported background-goroutine cancels/waitgroup and agent-touch debounce state)
}

func New(cfg config.Config) (*Service, error)
func NewWithEmbed(cfg config.Config, provider embed.Provider) (*Service, error)  // tests inject a deterministic provider
```

Note the architecture shift: the embedding `Worker` and the vec indexes are **not** Service-level singletons — they moved onto each `ProjectMount` so a server can host many projects with independent per-project embedders. `ProjectMount` carries `Store`, `RepoPath`, `Embed`, `EmbedDim`, `EmbedModel`, the four indexes (`SummaryIdx`, `TicketsIdx`, `LearningsIdx`, `CommentsIdx`), and its own `Worker`.

### Agents
- `RegisterAgent(ctx, key, name, metadata, requestedTTL time.Duration) (sessionID string, expiresAt time.Time, err error)`
- `Heartbeat(ctx, sessionID) (expiresAt time.Time, err error)`
- `GetAgent(ctx, id) (*domain.Agent, error)`

### Projects
- `CreateProject(ctx, slug, name, description, summary string) (*domain.Project, error)` — legacy path-implicit constructor that writes through `s.Store` (whichever `cfg.DataDir` resolved at startup). Used by the web handler and tests where the data dir is fixed at process start. Summary required, ≥200 chars after trim.
- `CreateProjectAt(ctx, repoPath, slug, name, description, summary string) (*domain.Project, error)` — explicit-path constructor used by the MCP `create_project` tool. The HTTP server has no cwd so the LLM declares the destination; `<repoPath>/.tickets_please/` is created if missing. **Auth-soft** (the bootstrap escape valve): no session required, `created_by` is left empty for the bootstrap call. After landing, the project is registered as a mount.
- `GetProject(ctx, idOrSlug string) (*domain.Project, error)`
- `ListProjects(ctx) ([]*domain.Project, error)` — across every mounted project.
- `UpdateProject(ctx, idOrSlug string, p UpdateProjectInput) (*domain.Project, error)`
- `DeleteProject(ctx, idOrSlug string) error` — unconditional. Removes every phase, ticket (active or done), comment, and embedding sidecar; unmounts the project; drops it from `<data_root>/registry.yaml`. Per-ticket "completion is sacred" is a per-ticket rule; project-level delete bypasses it.
- `LoadProject(ctx, idOrSlug string) (LoadProjectResult, error)` — explicit cache pre-warm; returns the project plus a diagnostic handle, expiry, and ticket counts (see the `LoadProjectResult` struct above)
- `RegisterProjectMount(ctx, repoPath string) (slug string, err error)` — read `<repoPath>/.tickets_please/project.yaml` and add a slug-keyed entry to the in-memory mount registry. Idempotent for the same `(repoPath, project UUID)` pair; LRU-evicts past `cfg.MaxLoadedProjects`. Persists the new path to `<data_root>/registry.yaml` so it survives a restart.
- `ResolveProjectStore(ctx, slug string) (*store.Store, error)` — return the live `*store.Store` for `slug`, lazy-re-mounting from the registry if the entry was LRU-evicted.

### Phases
- `CreatePhase(ctx, projectIDOrSlug, name, description, summary string) (*domain.Phase, error)`
- `GetPhase(ctx, projectIDOrSlug, phaseIDOrSlug string) (*domain.Phase, error)`
- `ListPhases(ctx, projectIDOrSlug string) ([]*domain.Phase, error)`
- `UpdatePhase(ctx, projectIDOrSlug, phaseIDOrSlug string, p UpdatePhaseInput) (*domain.Phase, error)`
- `DeletePhase(ctx, projectIDOrSlug, phaseIDOrSlug string) error` — refuses if any tickets are still assigned
- `ListWaves(ctx, projectIDOrSlug string, phaseIDOrSlug *string) ([]WaveSummary, error)` — `nil` phase = phase-less area

### Tickets
- `CreateTicket(ctx, in CreateTicketInput) (*domain.Ticket, error)` — always lands in `todo`. Carries optional `phase_id_or_slug`, `wave`, `depends_on`, `parallelizable_with`, and `kind` (`work` default, `idea` for spitballs — see *Ticket kinds*).
- `GetTicket(ctx, id string) (*domain.Ticket, error)`
- `ListTickets(ctx, in ListTicketsInput) (tickets []*domain.Ticket, nextCursor string, err error)` — supports `phase_id_or_slug`, `column`, `ready_only`, `wave` filter, pagination. `IncludeArchived` / `IncludeIdeas` opt those hidden kinds back in; `OnlyIdeas` inverts to ideas-only (backs `list_ideas`).
- `PromoteIdea(ctx, id, comment string, phaseIDOrSlug *string) (*domain.Ticket, error)` — flips a `kind=idea` ticket to `work` in place (keeps id/comments/embeddings), writes a `system_promote` comment, optionally assigns a phase. Refuses on a non-idea.
- `UpdateTicket(ctx, id string, in UpdateTicketInput) (*domain.Ticket, error)` — title/body/wave plus replace-set `depends_on` / `parallelizable_with`; no column.
- `MoveTicket(ctx, id string, target domain.Column, comment string) (*domain.Ticket, error)` — both required; rejects `done`.
- `CompleteTicket(ctx, id string, testingEvidence, workSummary, learnings string) (*domain.Ticket, error)` — `learnings` required (≥10 chars after trim); `testingEvidence` and `workSummary` are optional audit-trail fields (empty string accepted, no min length).
- `AssignTicketToPhase(ctx, id string, phaseIDOrSlug *string, comment string) (*domain.Ticket, error)` — `nil` = phase-less.
- `DeleteTicket(ctx, id string) error` — irreversibly removes a non-`done` ticket and its directory (body, comments, embedding sidecars). Refuses on `done` (preserves the no-reopen rule). Any other ticket in the same project whose `DependsOn` or `ParallelizableWith` slice contains the doomed id is rewritten in the same StageOp to drop the reference, so the cascade and the delete commit atomically — no dangling refs ever observed. Auto-commit captures the removal; no tombstone written.

### Comments
- `CreateComment(ctx, ticketID, body string) (*domain.Comment, error)` — always `kind=user`
- `ListComments(ctx, ticketID string) ([]*domain.Comment, error)` — includes `system_move` and `system_completion`
- `ListCommentsScoped(ctx, in ListCommentsScopedInput) ([]ScopedComment, string, error)` — project-wide (optionally narrowed to a phase or one ticket) with plain filters (author / `exclude_author_id`, `exclude_system`, `kinds`, `since`/`until`), ordered by `created_at`, cursor-paginated. Each result carries `ticket_id` + `ticket_title`.

### Search
- `SearchTickets(ctx, in SearchTicketsInput) ([]TicketHit, error)` — requires project filter in v1
- `SearchComments(ctx, in SearchCommentsInput) ([]CommentHit, error)`
- `SearchLearnings(ctx, in SearchLearningsInput) ([]LearningHit, error)` — over completed tickets only

There is no `SearchProjects` RPC or `search_projects` tool — project summaries are embedded (each mount's `SummaryIdx`), but cross-project summary search isn't exposed in v1.

### Domain types

Hand-written Go structs in `internal/domain/`. No code generation. Field semantics:

- `Project { ID, Slug, Name, Description, Summary string; CreatedBy *AgentRef; CreatedAt, UpdatedAt time.Time }`
- `Phase { ID, ProjectID, Slug string; Number int; Name, Description, Summary string; CreatedBy *AgentRef; CreatedAt, UpdatedAt time.Time; TicketCount, ActiveTicketCount int }`
- `Ticket { ID, ProjectID, Title, Body string; Column Column; Kind TicketKind; TestingEvidence, WorkSummary, Learnings *string; CompletedAt *time.Time; CreatedBy, CompletedBy *AgentRef; CreatedAt, UpdatedAt time.Time; DependsOn, ParallelizableWith, BlockedBy []string; PhaseID *string; Wave int; Archived bool; ArchivedAt *time.Time }`
- `WaveSummary { Wave int; TicketCount int; ActiveTicketCount int }`
- `Comment { ID, TicketID string; Kind CommentKind; Body string; FromColumn, ToColumn *Column; Author *AgentRef; CreatedAt time.Time }`
- `Agent { ID, Key, Name string; Metadata map[string]string; CreatedAt, ExpiresAt, LastSeenAt time.Time }`
- `AgentRef { ID, Name string }`
- `Column` — string typedef with constants `ColumnTodo / ColumnInProgress / ColumnTesting / ColumnDone`.
- `TicketKind` — string typedef with constants `KindWork / KindIdea`. Empty normalises to `KindWork` (`.OrWork()` on read; `.Stored()` collapses work→"" on write so work tickets omit the key).
- `CommentKind` — string typedef with constants `CommentKindUser / CommentKindSystemMove / CommentKindSystemCompletion / CommentKindSystemArchive / CommentKindSystemUnarchive / CommentKindSystemPromote`.

Errors are typed: `var ErrNotFound, ErrAlreadyExists, ErrFailedPrecondition, ErrInvalidArgument, ErrUnauthenticated`. Tools at the MCP layer translate these into MCP-friendly error codes.

### Key Go signatures

```go
type Column string
const (
    ColumnTodo       Column = "todo"
    ColumnInProgress Column = "in_progress"
    ColumnTesting    Column = "testing"
    ColumnDone       Column = "done"
)

func (s *Service) MoveTicket(ctx context.Context, ticketID string, target Column, comment string) (*domain.Ticket, error)
//   target must not be empty or ColumnDone; comment must be non-empty after trim.

func (s *Service) CompleteTicket(ctx context.Context, ticketID string, testingEvidence, workSummary, learnings string) (*domain.Ticket, error)
//   learnings must be ≥10 chars after strings.TrimSpace; testingEvidence and
//   workSummary are optional (empty string accepted). Empty optional fields
//   are omitted from completion.md / the system_completion comment.
```

`MoveTicket` rejects `ColumnDone` with `ErrInvalidArgument` and a message pointing at `CompleteTicket` — self-documenting for the LLM.

## Validation & enforcement

Two layers; server is authoritative.

1. **Service-level validation** (`internal/svc/validation.go`) — every method validates first. A 10-char minimum on `learnings` (the only required completion field; the other two are optional audit-trail strings) prevents thin "." satisfactions of the rule. Returns `domain.ErrInvalidArgument` with field-specific messages.
2. **`StageOp` ordered ops + per-project flock** — both rule-enforcing operations build a single StageOp under the project's flock and apply it as one batch:
   - `MoveTicket`: stage write of updated `ticket.yaml` + write of new `system_move` comment file. Apply phase renames both into place inside the locked window.
   - `CompleteTicket`: stage write of updated `ticket.yaml` + write of `completion.md` + write of `system_completion` comment file.

Failure mid-apply is detected by the integrity check (residual `.staging/<op-id>/`); the rules can't be silently bypassed because the caller never observes a half-applied state with the lock held.

## Embedding pipeline

**When**: async, in-process, fire-and-forget after the storage write commits. An LLM creating 20 tickets in a row shouldn't block on Ollama latency.

**How**: `internal/worker` runs a goroutine consuming a buffered `chan EmbedJob`. Server handlers enqueue after a successful storage write. The worker:
1. Calls `provider.Embed(ctx, text)`.
2. Writes the resulting `[]float32` to the appropriate `*.embedding.json` sidecar file (atomic write — temp file + rename).
3. Updates the in-memory `vecindex` so search reflects the change immediately.

Sidecar paths (relative to the data dir root — post-flatten there's one project per `.tickets_please/`):
- `summary.embedding.json` — for `summary.md`
- `tickets/<NNN>-…/body.embedding.json` — for `body.md`
- `tickets/<NNN>-…/learnings.embedding.json` — for the Learnings section of `completion.md`
- `tickets/<NNN>-…/comments/<filename>.embedding.json` — per comment

(Phase-scoped tickets sit under `phases/<NNN>-…/tickets/<NNN>-…/` and their sidecars sit beside them.)

The sidecar is a small JSON object that pairs the vector with the embedder identity, so a metadata-only read can answer "wrong embedder?" without parsing the float array: `{"provider": "ollama", "model": "bge-m3", "dim": 1024, "vec": [0.123, -0.456, ...]}`. `dim` is always `len(vec)` but persisted explicitly. There is intentionally no back-compat reader for the older flat-array shape — a cold clone rebuilds sidecars from source.

**Backfill**: on project load (or on full server start for resident indexes), the loader scans for source files without their sidecar:

```
for every source markdown/yaml file under the project,
  if no <stem>.embedding.json exists,
    enqueue an embed job
```

Covers crashes mid-job and re-embedding after `find -name '*.embedding.json' -delete`.

**Provider interface** (`internal/embed/provider.go`):

```go
type Provider interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    Dim() int
    Name() string
}
```

Default: Ollama + `bge-m3` (1024-dim). The dim is **not hardcoded**: each provider is probed at attach (`Probe()` embeds a token and records `len(vec)`), and every mount sizes its vec indexes to its provider's probed dim. A sidecar whose stamped `provider`/`model`/`dim` no longer matches its mount is treated as stale and rebuilt; `reembed_project` (or deleting `*.embedding.json` and letting the worker re-embed) is how you switch providers cleanly.

**What gets embedded**:
- Project: `summary.md` → `summary.embedding.json`. Re-embed on `UpdateProject(summary)`.
- Ticket: `title + "\n\n" + body.md` → `body.embedding.json`. Re-embed on `UpdateTicket`.
- Ticket on completion: the Learnings section of `completion.md` → `learnings.embedding.json`. Written once.
- Comment: comment file body (post-frontmatter) → comment's `*.embedding.json`. Set on insert (including system_move and system_completion entries — system comment bodies are searchable so the audit trail is queryable too).

## Vector search index

`internal/vecindex` keeps embeddings in memory for fast top-k cosine search. Brute-force is fine for our scale (thousands of 768/1024-dim vectors). Pluggable for HNSW later. The package is a stdlib-only leaf — no project imports.

```go
type Entry struct {
    ID    string             // source row id (project_id / ticket_id / comment_id)
    Kind  Kind               // the domain of the source row
    Owner string             // project slug; empty for global indexes
    Vec   []float32          // length = the provider's probed dim
}

type Index struct {
    mu      sync.RWMutex
    entries map[string]Entry  // id → entry
}

func (i *Index) Upsert(e Entry)
func (i *Index) Delete(id string)
func (i *Index) RemoveByOwner(owner string) int
func (i *Index) Search(query []float32, kind Kind, ownerFilter string, limit int) []Hit
```

Indexes are owned **per mount**, not globally resident. Each `ProjectMount` carries four `*vecindex.Index` instances — `SummaryIdx`, `TicketsIdx`, `LearningsIdx`, `CommentsIdx` — plus its own embedding worker that writes only into them. They're built when the mount attaches and freed (`RemoveByOwner` / drop) when it's LRU-evicted. `Service.defaultIndexes` is a resident fallback consulted only when the mount registry is empty (the stdio-with-no-mounts degenerate case) so unscoped search RPCs don't crash on nil indexes.

Cosine similarity assumes vectors are L2-normalized. Both Ollama (`bge-m3`) and OpenAI (`text-embedding-3-*`) return normalized vectors, so we don't normalize again.

## MCP server

When the LLM client spawns `tickets_please mcp` (the default subcommand of the single binary), the process: builds an in-process `svc.Service`, registers itself as an agent against that service, registers MCP tools that wrap the service methods, then serves stdio. Session lifecycle is handled internally — if the session expires mid-conversation the binary auto-re-registers via the cached identity; the LLM never sees session plumbing.

HTTP clients (centralised mode) connect via `/mcp` and **must** call `register_agent` once per connection to declare their identity and bind a `project_path`. After that, every `project_id_or_slug` parameter on subsequent tools becomes optional and falls back to the bound project. The one exception is `create_project`, which is auth-soft: an HTTP client with no project yet calls `create_project` first (passing `project_path`), then `register_agent` against the freshly-created project. Stdio clients pre-register at startup; they can still call `register_agent` to override the defaults.

Tools (descriptions written **for the model**, since they show up in tool listings). Canonical list — **40 tools** across projects, phases, tickets, ideas, comments, search, feedback, archive policy, and introspection. (The single source of truth is `expectedTools` in `internal/mcptools/tools_test.go`; keep this list, that list, and `RegisterAll` in lockstep.)

### Projects (8)

| Tool | Description |
|---|---|
| `list_projects` | List all ticket projects. Use this first to find the project you want to work in. |
| `create_project` | Create a new project. Slug must be unique and URL-safe. **Requires a `summary` field — a markdown document (≥200 chars) describing the project's goals, key components, and constraints.** Also requires `project_path` — the absolute filesystem path of the repo where the project should live; `<project_path>/.tickets_please/` will be created if it doesn't exist. This is the bootstrap mutation: no session required, so HTTP clients can call it before `register_agent`. |
| `get_project` | Fetch a project's full record (counts, attribution, timestamps, summary). |
| `get_project_summary` | Fetch just the project's summary markdown. **Read this before doing any non-trivial work in a project — it's the project's design context.** |
| `load_project` | Pre-warm a project into the server's in-memory cache. Useful before doing many operations against the same project. Optional — calls auto-load if needed. |
| `update_project` | Edit a project's name, description, or summary. Summary edits trigger re-embedding. |
| `delete_project` | **Irreversibly delete** a project and everything in it — every phase, every ticket (active or done), every comment, every embedding. The data dir survives but its project content is wiped, the project is unmounted, and it's removed from the persistent registry. Per-ticket completion immutability is a per-ticket rule; the project-level delete bypasses it. |
| `reembed_project` | Delete all `*.embedding.json` sidecars in a project and enqueue an async re-embed using the project's currently configured embedder. Use after switching `embed_provider`/`embed_model` in `project.yaml`, or to recover from corrupted sidecars. |

### Phases (8)

| Tool | Description |
|---|---|
| `list_phases` | List phases in a project with active and total ticket counts. |
| `create_phase` | Add a phase to a project for bigger bodies of work. Requires a `summary` (≥200 chars) — same load-bearing context doc as projects, scoped to this phase. |
| `get_phase` | Fetch a phase's full record. |
| `get_phase_summary` | Fetch a phase's full summary markdown. Read this when entering a phase, the same way you'd read a project summary. |
| `update_phase` | Edit a phase's name, description, or summary. |
| `delete_phase` | Delete a phase. Refuses if any tickets are still assigned to it. |
| `archive_phase` | Bulk-archive every active ticket in a phase in one call — the phase counterpart to `archive_ticket`. Each ticket gets its own `system_archive` audit comment (done tickets included), drops out of default search/list, and can be individually unarchived. The phase record is left in place (use `delete_phase` to remove an empty phase). Comment required; returns an archived-vs-skipped report. |
| `list_waves` | List the waves in a phase (or in the phase-less area of a project) with per-wave ticket counts. A wave is a soft integer grouping on tickets — no enforcement, just organization. Use this to see how a body of work decomposes. |

### Tickets (11)

| Tool | Description |
|---|---|
| `list_tickets` | List tickets in a project, optionally filtered by column or phase. Use `ready_only=true` to surface only unblocked tickets. Archived tickets and idea-kind tickets are excluded by default; pass `include_archived` / `include_ideas` to include them. |
| `create_ticket` | Create a new ticket in a project. Tickets always start in the `todo` column. Provide a clear title and a body that describes the work; both will be searchable. Optional `phase_id_or_slug`, `depends_on`, `parallelizable_with`. |
| `get_ticket` | Fetch a ticket by id, including its current column, completion fields if done, blockers, and who created/completed it. |
| `update_ticket` | Edit a ticket's title, body, wave, or dependency lists. `depends_on` / `parallelizable_with` use replace-set semantics when supplied. **Cannot** change the column — use `move_ticket` or `complete_ticket`. |
| `move_ticket` | Move a ticket between columns. Requires a comment explaining *why* you're moving it. Cannot be used to move to `done` — use `complete_ticket` for that. |
| `complete_ticket` | Mark a ticket done. Only `learnings` is required (≥10 chars) — that's the field future agents search, so write it for them. `testing_evidence` and `work_summary` are optional audit-trail fields; supply them when there's substantive content, omit on small/obvious work. |
| `assign_ticket_to_phase` | Move a ticket between phases (or to no phase). Requires a comment explaining why — same audit-trail rule as `move_ticket`. |
| `delete_ticket` | **Irreversibly delete** a non-`done` ticket and all of its body, comments, and embeddings. Refuses on `done` (completion is sacred — once finished, a ticket stays finished). Any other tickets that reference this one in `depends_on` or `parallelizable_with` are auto-updated to drop the reference, atomically with the delete — no dangling refs. For finished work that you regret, file a new ticket instead. |
| `archive_ticket` | Flip a ticket's separate `archived` flag without changing its column. Done tickets archive fine (completion fields stay frozen). Archived tickets drop out of default `search_*`/`list_tickets`; `include_archived: true` or `get_ticket` brings them back. Comment required (a `system_archive` audit comment). |
| `unarchive_ticket` | Reverse `archive_ticket` — the ticket re-enters default lists/search. Comment required (a `system_unarchive` audit comment). |
| `promote_idea` | Promote an idea-kind ticket into a real work ticket **in place** — the one forward path for an idea. Flips `kind: idea → work` while keeping the ticket's id, comments, and embedding history. Keeps the `todo` column; the ticket then appears on the default board. Refuses on a non-idea. Comment required (a `system_promote` audit comment); optional `phase_id_or_slug` drops it into a phase. |

### Ideas (3)

Ideas are tickets with `kind=idea` — a dedicated front door for spitballs (see *Ticket kinds* below). These are thin wrappers over the ticket store, no new storage.

| Tool | Description |
|---|---|
| `create_idea` | Throw a spitball: a lightweight idea you don't want to lose but aren't ready to action. Just `title` (+ optional `body`) — no deps/waves/phases. Lands in `todo` as `kind=idea`, hidden from the default work surfaces; promote it with `promote_idea` when it matures. |
| `list_ideas` | List the project's ideas — the spitball backlog, separate from the work board. `list_tickets` pinned to ideas only. |
| `search_ideas` | Semantic search over the project's ideas. `search_tickets` pinned to ideas only. |

#### Ticket kinds (ideation)

A ticket carries a `kind` — `work` (the default) or `idea` — an axis **orthogonal to `column` and `archived`**, mirroring how `archived` works:

- **Ideas are spitballs**, not work. They capture a half-formed thought without the lifecycle baggage a real ticket demands. They live in the `todo` column like any fresh ticket but are **hidden from the default work surfaces** (`list_tickets`, `search_tickets`, `ready_only`, the web board's columns) unless you opt in with `include_ideas: true`, or list them directly via `list_ideas` / `search_ideas`. This is the same default-hidden + opt-in shape as `archived` — and the two compose: an archived idea needs **both** `include_archived` and `include_ideas` to surface.
- **Backfill-free.** Empty `kind` normalises to `work`, so every pre-ideation `ticket.yaml` (which has no `kind:` key) loads as work with no migration. Work tickets never write a `kind:` key.
- **Lifecycle-gated.** An idea can't be `complete`d, can't walk out of `todo` via `move_ticket`, and can't be a `depends_on` / `parallelizable_with` target — each is rejected with a message pointing at `promote_idea`. The only forward path is promotion.
- **`promote_idea` flips `kind: idea → work` in place**, keeping the ticket's id, comments, and embedding history (no cross-store copy). The promoted ticket immediately joins the work board; an optional phase assignment can drop it into a phase in the same call. Promotion writes a `system_promote` audit comment.

This replaces the old anti-pattern of hijacking a real ticket (or a whole phase) to hold ideas — ideas now have their own first-class home that stays out of the work flow until you decide they're worth doing.

### Comments (3)

| Tool | Description |
|---|---|
| `add_comment` | Add a free-form comment to a ticket. Comments are immutable once created. |
| `list_comments` | List all comments on a ticket, including system-generated move and completion entries, with author attribution. |
| `list_comments_scoped` | List comments across a whole project (optionally narrowed to a phase or one ticket) with plain structured filters — author / `exclude_author_id`, `exclude_system` (default true), `kinds`, and a `since`/`until` created-at window — ordered by `created_at` and paginated via `cursor`. Each result carries `ticket_id` + `ticket_title`. The one-call way to surface operator feedback on recent work; complements `list_comments` (one ticket) and `search_comments` (semantic). |

### Search (3)

| Tool | Description |
|---|---|
| `search_tickets` | Semantic search over ticket titles and bodies in a project. Use when looking for related work. |
| `search_learnings` | Semantic search over completion learnings from past finished tickets. **Run this before starting non-trivial work — past you may have left notes.** |
| `search_comments` | Semantic search across comments. |

### Feedback (1)

| Tool | Description |
|---|---|
| `rate_search_result` | Thumbs-up / thumbs-down for one or more search results, keyed by the `entry_key` (`<kind>:<id>`) each hit carries. Aggregate counters live in per-project `feedback.yaml` (git-tracked) and feed the W2 ranking multiplier + W3 archival policy. Per-key partial success — a malformed or unknown key fails just that key, not the whole call. No per-rating ticket comment is created (would be noisy at scale); `feedback.yaml`'s git history is the audit trail. |

### Archive (1)

| Tool | Description |
|---|---|
| `apply_archive_policy` | Run the per-project archive policy. **Dry-run by default**; pass `commit: true` to actually flip the flags. Returns `considered`, `would_archive`, `archived`, `skipped`, and the resolved policy `config`. Refuses when the project's `archive.enabled: false` is set (opt-in gate). Each committed archive writes a `system_archive` audit comment with the decision reason. |

#### Search ranking with feedback (W2)

Each search hit's `score` is the **final** ranking score, computed as:

```
quality    = (likes + alpha) / (likes + dislikes + alpha + beta)   // ∈ (0, 1)
multiplier = min_multiplier + (1 - min_multiplier) * quality        // ∈ [min_multiplier, 1.0]
final      = cosine_similarity * multiplier                         // when cosine > 0
```

Defaults are `α=β=2`, `min_multiplier=0.5`, `enabled=true`. An unrated entry has `quality=0.5 → multiplier=0.75`; a heavily-liked entry approaches multiplier `1.0`; a heavily-disliked entry floors at `0.5`. Negative cosine (anti-correlated) passes through unchanged so the multiplier can't promote a poor match.

Each hit also surfaces `raw_score` — the pre-multiplier cosine — so external tooling can see the delta the multiplier introduced.

Per-project override in `project.yaml`:

```yaml
feedback:
  alpha: 2.0
  beta: 2.0
  min_multiplier: 0.5
  enabled: true   # off → behave as today (cosine-only)
```

All fields optional; missing values inherit the defaults. `enabled: false` is the kill switch.

#### Archive policy (W3)

Per-project archive block in `project.yaml` (all fields optional; opt-in via `enabled: true`):

```yaml
archive:
  enabled: false               # opt-in per project
  min_age_days: 180            # only consider entries this old or older
  min_retrievals: 3            # ...AND retrieved at least this many times
  dislike_ratio: 0.5           # dislikes / (likes + dislikes) ≥ this → early-archive eligible
  early_archive_age_days: 30   # ...but not earlier than this
  auto_sweep_on_mount: false   # T6 hook; run the sweep at mount time
```

Decision matrix (per ticket):

```
NEVER archive if already archived OR policy disabled.
ARCHIVE (aged branch) if age ≥ min_age_days
                    AND retrievals ≥ min_retrievals
                    AND (total_ratings == 0 OR dislike_ratio ≥ threshold).
EARLY ARCHIVE if age ≥ early_archive_age_days
              AND total_ratings ≥ 3
              AND dislike_ratio ≥ threshold.
```

Archive flag is **independent of column** — a `done` ticket can be archived (its completion fields stay frozen; only the flag flips). Archived tickets are post-filtered out of `search_*` and `list_tickets` by default; `include_archived: true` brings them back. `get_ticket` returns archived tickets unconditionally. Vec index entries stay in place, so unarchive is free (no re-embed). Manual archive/unarchive route through the `archive_ticket` / `unarchive_ticket` tools.

`apply_archive_policy` walks the candidate set and produces a dry-run report by default (`commit: true` actually flips the flags). Refuses when `archive.enabled: false`. `archive.auto_sweep_on_mount: true` opts the project into a background sweep that fires after each mount-hydrate completes — guarded by a per-mount mutex so overlapping triggers don't stampede, and skipped silently when the policy is disabled. The sweep emits a structured log line with `considered`, `archived`, `skipped`, and `took` counts.

### Introspection (2)

| Tool | Description |
|---|---|
| `who_am_i` | Returns the current agent identity the MCP server has registered for this session, including the bound project (if any) and the session's expiry. Useful for the LLM to confirm its own attribution before doing work. |
| `register_agent` | Self-register this MCP session: declare the model, client, and bound project. **HTTP clients must call this once on connection** before any other mutating tool. Stdio clients pre-register at startup and only need it to override the defaults. The `project_path` must be the absolute path to a repo containing `.tickets_please/project.yaml`; the server validates it, mounts the project (idempotent), and binds the slug to this session. Subsequent `project_id_or_slug` parameters then become optional. |

The "run `search_learnings` first" and "read `get_project_summary` before working" instructions are the unlocks that make the system feed itself.
