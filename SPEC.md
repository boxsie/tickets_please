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
| MCP SDK | `github.com/mark3labs/mcp-go` (stdio transport) |
| File watching | `github.com/fsnotify/fsnotify` |
| File locking | `golang.org/x/sys/unix` flock (Linux/macOS) — Windows path-locking left as a future concern |
| Embeddings | Ollama + `nomic-embed-text` (768-dim, local, default) — pluggable for OpenAI |
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
- `tickets_please check` — run integrity check + exit.
- `tickets_please init` — create `.tickets_please/` skeleton.
- *(future)* `tickets_please serve` — long-running daemon for a future web frontend. Not in v1.

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

Mutations acquire an OS-level exclusive lock on a per-project lock file before writing:

- **Project-scoped mutation** (CreateTicket, MoveTicket, CreateComment, etc.) → `flock(.tickets_please/projects/<slug>/.lock, LOCK_EX)` for the duration of `StageOp.Commit`.
- **Cross-project mutation** (CreateProject, DeleteProject, anything touching the project list) → `flock(.tickets_please/.lock, LOCK_EX)` briefly.
- **Reads do not lock.** They rely on atomic-write semantics for consistency.

Implementation: thin wrapper `internal/store/lock.go` using `golang.org/x/sys/unix.Flock`. Locks release automatically on file close (or process death — the kernel reclaims). Acquisition has a configurable timeout (default 10s) to surface deadlocks rather than hang.

Two MCPs working on **different projects**: zero contention. Two MCPs racing on the **same project**: serialized at the lock; correct semantics. Linux/macOS handle this natively; Windows is a future concern (the project's hobby/play scope makes that fine).

### Layer 3 — `fsnotify` cache invalidation

Cross-process consistency without polling. When a process loads a project into its in-memory cache, it sets up an `fsnotify` watcher on `projects/<slug>/`. Any change (from any process) emits an event; the cache:

1. Marks the project as "stale" (a flag, not an immediate evict).
2. The next call into that project reloads from disk.
3. If a write was in flight from this process, the watcher correlates and skips the invalidation (compare to a recent op-id).

Latency is ~10ms in practice on Linux. This is what actually makes multi-process coexistence safe — flock alone wouldn't catch a stale cache.

The `LoadedProject` struct gains a `Stale atomic.Bool` field; reads check it before serving.

### What this does NOT solve

- **Cross-machine sync.** Syncing `.tickets_please/` via Dropbox/Syncthing while two machines are simultaneously writing is out of scope. That's an OS-level FS coherence problem, not a data-store problem.
- **A misbehaving process holding the project lock indefinitely.** Surfaces as a "lock acquisition timed out after 10s" log line; the user can `kill` the bad process. Kernel releases the lock on death.
- **Windows.** The flock primitive used (`syscall.Flock` / `unix.Flock`) is POSIX. Windows path-locking is different and deferred — works fine on macOS and Linux, which is plenty for the hobby scope.

### Configuration

| Key | Env var | Default | Notes |
|---|---|---|---|
| `lock_timeout_seconds` | `LOCK_TIMEOUT_SECONDS` | `10` | How long to wait for a project/global lock before erroring. |
| `fsnotify_enabled` | `FSNOTIFY_ENABLED` | `true` | Set false to fall back to mtime polling on every read (slower, simpler). |

## Design decisions

- **Embedding default**: Ollama + `nomic-embed-text` (768-dim). Provider is an interface; OpenAI impl ships alongside, switchable via the `embed_provider` config key (or `EMBED_PROVIDER` env var). Dim mismatch vs. schema fails loud at startup.
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

- `projects/<slug>/project.yaml` → `created_by: <agent-uuid>` (or `null`)
- `projects/<slug>/tickets/<NNN>-…/ticket.yaml` → `created_by`, `completed_by` (each nullable)
- `projects/<slug>/phases/<NNN>-…/phase.yaml` → `created_by` (nullable)
- comment frontmatter → `author_id: <agent-uuid>` (or `null`)

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
| `data_dir` | `DATA_DIR` | `./.tickets_please` | Root of the on-disk data tree. Defaults to a directory in the cwd. |
| `auto_commit` | `AUTO_COMMIT` | `true` | If true and `data_dir` is inside a git repo, every mutation produces a commit authored as the calling agent. |
| `embed_provider` | `EMBED_PROVIDER` | `ollama` | `ollama` or `openai`. |
| `ollama_url` | `OLLAMA_URL` | `http://localhost:11434` | Used when `embed_provider=ollama`. |
| `ollama_model` | `OLLAMA_MODEL` | `nomic-embed-text` | Used when `embed_provider=ollama`. |
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
├── Makefile                       # build, run, test, init-config, init-data, check
├── SPEC.md                        # this file
├── examples/config.yaml           # sample config users copy to ~/.tickets_please/
├── .tickets_please/               # default data dir (committed; this is the data!)
│   └── README.md                  # explains the layout to anyone clicking around
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
│   │   ├── agents.go
│   │   └── integrity.go           # startup integrity check
│   ├── cache/                     # project cache with sliding TTL eviction
│   ├── vecindex/                  # in-memory vector index (cosine, brute-force)
│   ├── embed/                     # provider interface + impls
│   │   ├── provider.go
│   │   ├── ollama.go
│   │   └── openai.go
│   ├── worker/                    # async embedding worker
│   ├── svc/                       # business logic — methods on Service{}
│   │   ├── service.go             # Service struct, New()
│   │   ├── middleware.go          # session-validating in-process middleware
│   │   ├── projects.go
│   │   ├── phases.go
│   │   ├── tickets.go
│   │   ├── comments.go
│   │   ├── search.go
│   │   ├── agents.go
│   │   └── validation.go
│   └── mcptools/                  # mark3labs/mcp-go tool wrappers around svc
│       ├── tools.go
│       ├── format.go              # domain → LLM-friendly JSON
│       └── identity.go            # MCP self-registration
└── cmd/
    └── tickets_please/main.go     # one binary; CLI dispatches subcommands (mcp default)
```

Go module name: `tickets_please`. **One binary**, subcommand-dispatched. No `proto/`, no `gen/`, no `buf*` configs, no separate gRPC server.

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

```
projects/<slug>/
├── project.yaml
├── summary.md
├── summary.embedding.json
├── tickets/                     # phase-less tickets sit here
│   └── <NNN>-<slug>/
└── phases/                      # only present when the project has phases
    └── <NNN>-<phase-slug>/
        ├── phase.yaml
        ├── summary.md
        ├── summary.embedding.json
        └── tickets/
            └── <NNN>-<ticket-slug>/
```

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
- Stored on the `projects` row in the `summary` column (text) and embedded into `summary_embedding` (vector(768)) so future projects can semantically discover related work.
- Exposed on the `Project` message so it travels with every read.
- MCP has a dedicated `get_project_summary` tool whose description tells the LLM: *"Read this before starting work in a project — it's the project's design context."*
- Editable via `UpdateProject(summary?)` — re-embedding is triggered on change. Edits don't carry forward through git-style history; the latest summary wins. (Comments form the audit trail; the summary is intentional living documentation.)

## Data layout

All data lives under `data_dir` (default `./.tickets_please/`). The tree is the source of truth; in-memory state is reconstructable from the files on disk.

```
.tickets_please/
├── README.md                                # short orientation for anyone browsing the repo
├── agents/
│   └── <session-uuid>.yaml                  # one file per session (active or expired)
├── projects/
│   └── <slug>/
│       ├── project.yaml                     # id, slug, name, description, created_by, created_at, updated_at
│       ├── summary.md                       # the required markdown summary (≥ 200 chars)
│       ├── summary.embedding.json           # 768-float JSON array
│       └── tickets/
│           └── <NNN>-<slugified-title>/
│               ├── ticket.yaml              # id, title, column, body_path, created_by, completed_by, completed_at, created_at, updated_at
│               ├── body.md
│               ├── body.embedding.json
│               ├── completion.md            # only when column == done
│               ├── learnings.embedding.json # only when column == done
│               └── comments/
│                   ├── <ts>-<short-id>-<kind>.md           # one file per comment
│                   └── <ts>-<short-id>-<kind>.embedding.json
└── .staging/                                # transient atomicity dir; emptied on graceful shutdown
```

### File formats

**`project.yaml`** *(example)*:

```yaml
id: 7e2f4a4d-9c4b-4a1e-9b2f-2c5e9a3b6d11
slug: tickets_please
name: tickets_please
description: A Trello-like ticketing system designed for LLM agents.
created_by: 8a51c2c0-22ad-4e7c-92d1-f9d6e7a17b50    # agent.id
created_at: 2026-05-02T13:42:11.123Z
updated_at: 2026-05-02T13:42:11.123Z
```

**`ticket.yaml`** *(example)*:

```yaml
id: c0a55d8c-3d63-4f6a-b3a7-9e8a1d8c2f44
project_slug: tickets_please
number: 7
title: Implement MoveTicket transactional flow
column: in_progress           # one of: todo, in_progress, testing, done
created_by: 8a51c2c0-22ad-4e7c-92d1-f9d6e7a17b50
completed_by: null
completed_at: null
created_at: 2026-05-02T13:50:01.000Z
updated_at: 2026-05-02T14:11:09.000Z
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
kind: system_move
author_id: 8a51c2c0-22ad-4e7c-92d1-f9d6e7a17b50
from_column: todo
to_column: in_progress
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
    Phases        map[string]*domain.Phase     // populated when phases exist (T16)
    Tickets       map[string]*domain.Ticket    // ticket id → ticket (yaml + body.md + completion.md if done)
    Comments      map[string][]*domain.Comment // ticket id → ordered comment list
    LoadedAt      time.Time
    LastAccessAt  time.Time
    Stale         atomic.Bool                  // flipped by fsnotify when files change cross-process
    Lock          sync.RWMutex
    // Per-project vector index attaches here later — owned by T11 (search). Excluded from T04
    // so the cache compiles before vecindex/embed/worker packages exist.
}
```

The cross-project `SearchLearnings` and `SearchProjects` indexes are always-resident (their working sets are small and their utility is global). Per-project indexes (when added by T11) are partitioned so eviction frees their memory cleanly.

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
- `ListTickets(..., ready_only)` — when true, filters to tickets with empty `BlockedBy` and column ∈ {todo, in_progress}.
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
    Store    *store.Store
    Cache    *cache.ProjectCache
    Embed    embed.Provider
    Worker   *worker.Worker
    LearningsIdx *vecindex.Index   // resident
    SummaryIdx   *vecindex.Index   // resident
    Logger   *slog.Logger
    Cfg      config.Config
}

func New(cfg config.Config) (*Service, error)
```

### Agents
- `RegisterAgent(ctx, key, name, metadata, requestedTTL time.Duration) (sessionID string, expiresAt time.Time, err error)`
- `Heartbeat(ctx, sessionID) (expiresAt time.Time, err error)`
- `GetAgent(ctx, id) (*domain.Agent, error)`

### Projects
- `CreateProject(ctx, slug, name, description, summary string) (*domain.Project, error)` — summary required, ≥200 chars after trim
- `GetProject(ctx, idOrSlug string) (*domain.Project, error)`
- `ListProjects(ctx) ([]*domain.Project, error)`
- `UpdateProject(ctx, idOrSlug string, p UpdateProjectInput) (*domain.Project, error)`
- `DeleteProject(ctx, idOrSlug string) error`
- `LoadProject(ctx, idOrSlug string) (*cache.LoadedProject, error)` — explicit cache pre-warm

### Phases
- `CreatePhase(ctx, projectIDOrSlug, name, description, summary string) (*domain.Phase, error)`
- `GetPhase(ctx, projectIDOrSlug, phaseIDOrSlug string) (*domain.Phase, error)`
- `ListPhases(ctx, projectIDOrSlug string) ([]*domain.Phase, error)`
- `UpdatePhase(ctx, projectIDOrSlug, phaseIDOrSlug string, p UpdatePhaseInput) (*domain.Phase, error)`
- `DeletePhase(ctx, projectIDOrSlug, phaseIDOrSlug string) error` — refuses if any tickets are still assigned
- `ListWaves(ctx, projectIDOrSlug string, phaseIDOrSlug *string) ([]WaveSummary, error)` — `nil` phase = phase-less area

### Tickets
- `CreateTicket(ctx, in CreateTicketInput) (*domain.Ticket, error)` — always lands in `todo`. Carries optional `phase_id_or_slug`, `wave`, `depends_on`, `parallelizable_with`.
- `GetTicket(ctx, id string) (*domain.Ticket, error)`
- `ListTickets(ctx, in ListTicketsInput) (tickets []*domain.Ticket, nextCursor string, err error)` — supports `phase_id_or_slug`, `column`, `ready_only`, `wave` filter, pagination.
- `UpdateTicket(ctx, id string, in UpdateTicketInput) (*domain.Ticket, error)` — title/body only; no column.
- `MoveTicket(ctx, id string, target domain.Column, comment string) (*domain.Ticket, error)` — both required; rejects `done`.
- `CompleteTicket(ctx, id string, testingEvidence, workSummary, learnings string) (*domain.Ticket, error)` — all three required, ≥10 chars each.
- `AssignTicketToPhase(ctx, id string, phaseIDOrSlug *string, comment string) (*domain.Ticket, error)` — `nil` = phase-less.

### Comments
- `CreateComment(ctx, ticketID, body string) (*domain.Comment, error)` — always `kind=user`
- `ListComments(ctx, ticketID string) ([]*domain.Comment, error)` — includes `system_move` and `system_completion`

### Search
- `SearchProjects(ctx, query string, limit int) ([]ProjectHit, error)`
- `SearchTickets(ctx, in SearchTicketsInput) ([]TicketHit, error)` — requires project filter in v1
- `SearchComments(ctx, in SearchCommentsInput) ([]CommentHit, error)`
- `SearchLearnings(ctx, in SearchLearningsInput) ([]LearningHit, error)` — over completed tickets only

### Domain types

Hand-written Go structs in `internal/domain/`. No code generation. Field semantics:

- `Project { ID, Slug, Name, Description, Summary string; CreatedBy *AgentRef; CreatedAt, UpdatedAt time.Time }`
- `Phase { ID, ProjectID, Slug string; Number int; Name, Description, Summary string; CreatedBy *AgentRef; CreatedAt, UpdatedAt time.Time; TicketCount, ActiveTicketCount int }`
- `Ticket { ID, ProjectID, Title, Body string; Column Column; TestingEvidence, WorkSummary, Learnings *string; CompletedAt *time.Time; CreatedBy, CompletedBy *AgentRef; CreatedAt, UpdatedAt time.Time; DependsOn, ParallelizableWith, BlockedBy []string; PhaseID *string; Wave int }`
- `WaveSummary { Wave int; TicketCount int; ActiveTicketCount int }`
- `Comment { ID, TicketID string; Kind CommentKind; Body string; FromColumn, ToColumn *Column; Author *AgentRef; CreatedAt time.Time }`
- `Agent { ID, Key, Name string; Metadata map[string]string; CreatedAt, ExpiresAt, LastSeenAt time.Time }`
- `AgentRef { ID, Name string }`
- `Column` — string typedef with constants `ColumnTodo / ColumnInProgress / ColumnTesting / ColumnDone`.
- `CommentKind` — string typedef with constants `CommentKindUser / CommentKindSystemMove / CommentKindSystemCompletion`.

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
//   each of the three fields must be ≥10 chars after strings.TrimSpace.
```

`MoveTicket` rejects `ColumnDone` with `ErrInvalidArgument` and a message pointing at `CompleteTicket` — self-documenting for the LLM.

## Validation & enforcement

Two layers; server is authoritative.

1. **Service-level validation** (`internal/svc/validation.go`) — every method validates first. Min lengths (10 chars) on completion fields prevent thin one-character "satisfactions" of the rule. Returns `domain.ErrInvalidArgument` with field-specific messages.
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

Sidecar paths:
- `projects/<slug>/summary.embedding.json` — for `summary.md`
- `projects/<slug>/tickets/<NNN>-…/body.embedding.json` — for `body.md`
- `projects/<slug>/tickets/<NNN>-…/learnings.embedding.json` — for the Learnings section of `completion.md`
- `projects/<slug>/tickets/<NNN>-…/comments/<filename>.embedding.json` — per comment

JSON format is a flat array, no metadata: `[0.123, -0.456, ...]`. The dim is implicit (768).

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

Default: Ollama. The vec index is dim-agnostic per project but the **server** asserts `provider.Dim() == 768` at startup so loaded sidecars match. Switching providers with a different dim requires deleting old `*.embedding.json` files and letting the worker re-embed.

**What gets embedded**:
- Project: `summary.md` → `summary.embedding.json`. Re-embed on `UpdateProject(summary)`.
- Ticket: `title + "\n\n" + body.md` → `body.embedding.json`. Re-embed on `UpdateTicket`.
- Ticket on completion: the Learnings section of `completion.md` → `learnings.embedding.json`. Written once.
- Comment: comment file body (post-frontmatter) → comment's `*.embedding.json`. Set on insert (including system_move and system_completion entries — system comment bodies are searchable so the audit trail is queryable too).

## Vector search index

`internal/vecindex` keeps embeddings in memory for fast top-k cosine search. Brute-force is fine for our scale (thousands of vectors at 768-dim). Pluggable for HNSW later.

```go
type Entry struct {
    ID    string             // source row id
    Kind  Kind               // project | ticket | learning | comment
    Owner string             // project slug; for cross-project indexes
    Vec   []float32          // 768
}

type Index struct {
    entries map[string]Entry  // id → entry
    mu      sync.RWMutex
}

func (i *Index) Upsert(e Entry)
func (i *Index) Delete(id string)
func (i *Index) Search(query []float32, kind Kind, project string, limit int) []Hit
```

There are two index instances:
- **Per-project** (lives inside `LoadedProject.Vectors`) — built when the project is loaded; freed on eviction. Used by `SearchTickets` and `SearchComments` when scoped to a project.
- **Global resident** — `learnings_index` and `projects_summary_index`. Always loaded at startup (the working sets are small) and updated incrementally on completion / project mutation. Used by `SearchLearnings` and `SearchProjects`.

Cosine similarity assumes vectors are L2-normalized. Both Ollama (`nomic-embed-text`) and OpenAI (`text-embedding-3-*`) return normalized vectors, so we don't normalize again.

## MCP server

When the LLM client spawns `tickets_please mcp` (the default subcommand of the single binary), the process: builds an in-process `svc.Service`, registers itself as an agent against that service, registers MCP tools that wrap the service methods, then serves stdio. Session lifecycle is handled internally — if the session expires mid-conversation the binary auto-re-registers; the LLM never sees session plumbing.

Tools (descriptions written **for the model**, since they show up in tool listings). Canonical list — **28 tools** across projects, phases, tickets, comments, search, and introspection.

### Projects (7)

| Tool | Description |
|---|---|
| `list_projects` | List all ticket projects. Use this first to find the project you want to work in. |
| `create_project` | Create a new project. Slug must be unique and URL-safe. **Requires a `summary` field — a markdown document (≥200 chars) describing the project's goals, key components, and constraints.** This summary becomes the load-bearing context any future agent reads before working in this project. Be thorough. |
| `get_project` | Fetch a project's full record (counts, attribution, timestamps, summary). |
| `get_project_summary` | Fetch just the project's summary markdown. **Read this before doing any non-trivial work in a project — it's the project's design context.** |
| `load_project` | Pre-warm a project into the server's in-memory cache. Useful before doing many operations against the same project. Optional — calls auto-load if needed. |
| `update_project` | Edit a project's name, description, or summary. Summary edits trigger re-embedding. |
| `delete_project` | Delete a project. Refuses if any tickets are still active. |

### Phases (7)

| Tool | Description |
|---|---|
| `list_phases` | List phases in a project with active and total ticket counts. |
| `create_phase` | Add a phase to a project for bigger bodies of work. Requires a `summary` (≥200 chars) — same load-bearing context doc as projects, scoped to this phase. |
| `get_phase` | Fetch a phase's full record. |
| `get_phase_summary` | Fetch a phase's full summary markdown. Read this when entering a phase, the same way you'd read a project summary. |
| `update_phase` | Edit a phase's name, description, or summary. |
| `delete_phase` | Delete a phase. Refuses if any tickets are still assigned to it. |
| `list_waves` | List the waves in a phase (or in the phase-less area of a project) with per-wave ticket counts. A wave is a soft integer grouping on tickets — no enforcement, just organization. Use this to see how a body of work decomposes. |

### Tickets (7)

| Tool | Description |
|---|---|
| `list_tickets` | List tickets in a project, optionally filtered by column or phase. Use `ready_only=true` to surface only unblocked tickets. |
| `create_ticket` | Create a new ticket in a project. Tickets always start in the `todo` column. Provide a clear title and a body that describes the work; both will be searchable. Optional `phase_id_or_slug`, `depends_on`, `parallelizable_with`. |
| `get_ticket` | Fetch a ticket by id, including its current column, completion fields if done, blockers, and who created/completed it. |
| `update_ticket` | Edit a ticket's title or body. **Cannot** change the column — use `move_ticket` or `complete_ticket`. |
| `move_ticket` | Move a ticket between columns. Requires a comment explaining *why* you're moving it. Cannot be used to move to `done` — use `complete_ticket` for that. |
| `complete_ticket` | Mark a ticket done. Requires `testing_evidence` (what you tested and how), `work_summary` (what you actually changed), `learnings` (gotchas, surprises, insights for future work). Be thorough — `learnings` are searchable by future tickets. |
| `assign_ticket_to_phase` | Move a ticket between phases (or to no phase). Requires a comment explaining why — same audit-trail rule as `move_ticket`. |

### Comments (2)

| Tool | Description |
|---|---|
| `add_comment` | Add a free-form comment to a ticket. Comments are immutable once created. |
| `list_comments` | List all comments on a ticket, including system-generated move and completion entries, with author attribution. |

### Search (4)

| Tool | Description |
|---|---|
| `search_projects` | Semantic search over project summaries. Use when picking a project to work in or finding related projects. |
| `search_tickets` | Semantic search over ticket titles and bodies in a project. Use when looking for related work. |
| `search_learnings` | Semantic search over completion learnings from past finished tickets. **Run this before starting non-trivial work — past you may have left notes.** |
| `search_comments` | Semantic search across comments. |

### Introspection (1)

| Tool | Description |
|---|---|
| `who_am_i` | Returns the current agent identity the MCP server has registered for this session. Useful for the LLM to confirm its own attribution before doing work. |

The "run `search_learnings` first" and "read `get_project_summary` before working" instructions are the unlocks that make the system feed itself.
