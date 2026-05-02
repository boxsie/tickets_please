---
id: T02
title: Storage primitives & integrity check
status: DONE
owner: subagent-T02
depends_on: [T01]
parallelizable_with: [T03, T08, T15]
wave: 1
files:
  - internal/store/store.go
  - internal/store/stage.go
  - internal/store/lock.go
  - internal/store/watch.go
  - internal/store/yaml.go
  - internal/store/frontmatter.go
  - internal/store/projects.go
  - internal/store/phases.go
  - internal/store/tickets.go
  - internal/store/comments.go
  - internal/store/agents.go
  - internal/store/integrity.go
  - internal/store/git.go
estimate: medium
stretch: false
---

# T02 — Storage primitives & integrity check

## Scope

Build the filesystem storage layer that every other ticket reads/writes through. Atomic writes via staging-dir + rename, **per-project flock for mutations**, **fsnotify for cross-process cache invalidation**, yaml + frontmatter codecs, walk helpers, startup integrity check, and an opt-out git auto-commit hook.

**In:** `internal/store/` package, startup integrity walk wired into the `check` subcommand, file-watching helpers exposed for the project cache (T04) to consume.

**Out:** No svc methods, no embedding work, no project cache. T03 owns the `internal/domain/` types. T04 builds on Store; T09/T10 add embeddings; T15 adds agent identity.

## Files

- `internal/store/store.go` — `Store` struct rooted at data_dir
- `internal/store/stage.go` — `StageOp` helper: stage paths under `.staging/<op-id>/`, rename into place atomically, cleanup on failure
- `internal/store/lock.go` — `WithProjectLock(slug, fn)` and `WithGlobalLock(fn)` using `golang.org/x/sys/unix.Flock` with timeout
- `internal/store/watch.go` — `WatchProject(slug, onChange func())` wrapping `fsnotify`
- `internal/store/yaml.go` — yaml read/write helpers (gopkg.in/yaml.v3)
- `internal/store/frontmatter.go` — markdown-with-yaml-frontmatter codec for comment files
- `internal/store/projects.go` — read/write/walk projects
- `internal/store/phases.go` — read/write/walk phases within a project
- `internal/store/tickets.go` — read/write/walk tickets within a project (handles both phased and phase-less paths)
- `internal/store/comments.go` — append-only comment writes; walk in chronological order
- `internal/store/agents.go` — read/write/walk agents; active-session uniqueness check
- `internal/store/integrity.go` — startup integrity walk
- `internal/store/git.go` — `Commit(ctx, paths, agent, summary)` using go-git; no-op when not in a git repo or `auto_commit: false`

## Details

### `Store` shape

```go
type Store struct {
    Root       string         // absolute path to data_dir
    AutoCommit bool
    Logger     *slog.Logger
}

func New(cfg config.Config) (*Store, error)
```

`New` resolves `cfg.DataDir` to an absolute path, creates `agents/`, `projects/`, `.staging/` if missing, runs an integrity check, and (if applicable) verifies the dir is inside a git repo.

### Per-project locks (`lock.go`)

```go
func (s *Store) WithProjectLock(ctx context.Context, slug string, fn func() error) error
func (s *Store) WithGlobalLock(ctx context.Context, fn func() error) error
```

- Each acquires `unix.Flock(fd, LOCK_EX)` on `<root>/projects/<slug>/.lock` (or `<root>/.lock` for global).
- Lock acquisition has a configurable timeout (`cfg.LockTimeoutSeconds`, default 10s) implemented by repeatedly trying with `LOCK_EX|LOCK_NB` and sleeping 50ms between attempts.
- On timeout: return an error like `lock contention on project <slug> (held > 10s)` so the caller can surface it cleanly.
- Lock released on file close OR process death (kernel cleans up).
- Reads do **not** lock; they rely on atomic-write semantics.

The `StageOp.Commit` flow described below acquires the appropriate lock before performing renames.

### File watching (`watch.go`)

Lightweight `fsnotify` wrapper:

```go
type ProjectWatcher struct {
    Slug    string
    Events  chan struct{}  // collapsed change signal; one per debounce window
    Close   func()
}

func (s *Store) WatchProject(slug string) (*ProjectWatcher, error)
```

- Recursively watches `projects/<slug>/`.
- Coalesces bursts of events into a single signal (debounce ~50ms).
- Filters out events from `.staging/` and `.lock` files.
- Used by the project cache (T04) to invalidate `LoadedProject.Stale` on cross-process changes.

### Atomic writes via `StageOp` (ordered ops, not flat file map)

Multi-file mutations need more than just "write these N files atomically". `AssignTicketToPhase` (T16) renames an entire ticket directory between `tickets/` and `phases/<NNN>-…/tickets/` AND rewrites `ticket.yaml` AND inserts a `system_move` comment. To support this, `StageOp` is an ordered list of typed ops — not a flat map.

```go
type StageOp struct {
    OpID string
    ops  []op
    dir  string // .staging/<op-id>/
}

type op interface{ prepare(stagingDir string) error; apply(rootDir string) error }

type writeOp struct  { path string; content []byte }
type renameOp struct { from, to string }   // both relative to root
type removeOp struct { path string }       // relative to root; works on files or trees

// Methods on *StageOp:
func (s *Store) BeginOp() *StageOp
func (o *StageOp) Write(relPath string, content []byte)        // appends a writeOp
func (o *StageOp) RenameDir(fromRel, toRel string)             // appends a renameOp
func (o *StageOp) RemovePath(relPath string)                   // appends a removeOp
func (o *StageOp) Commit(ctx context.Context, lock LockScope, agent *domain.Agent, summary string) error
func (o *StageOp) Abort()
```

`Write` immediately performs `os.WriteFile` + `f.Sync()` against `.staging/<op-id>/<relPath>` (mkdir parents). `RenameDir`/`RemovePath` register intent only — no fs change at append time, so a crash before `Commit` leaves at most some staged file content (which the integrity check picks up).

`Commit` flow:
1. Acquire the appropriate flock (`LockScope` is `LockProject(slug)` or `LockGlobal`).
2. **Apply phase** — iterate `ops` in declared order:
   - `writeOp`: `os.MkdirAll` parent of final, then `os.Rename` from staging → final.
   - `renameOp`: `os.Rename(fromAbs, toAbs)` — single syscall, atomic per rename.
   - `removeOp`: `os.RemoveAll(absPath)`.
3. `os.RemoveAll(.staging/<op-id>/)`.
4. If `s.AutoCommit && agent != nil`, call `git.Commit(ctx, touchedPaths, agent, summary)` (inside the lock — keeps audit-trail commits ordered).
5. Release the lock.

Failure mid-apply leaves a partial state — but only across distinct ops, never within one (each underlying syscall is atomic). The integrity check at next startup finds:
- `.staging/<op-id>/` not removed → operation didn't finish, paths can be inspected.
- A ticket with a bad `column` vs. its directory location (e.g. `done` ticket missing `completion.md`) → partial completion.

The honest claim isn't "multi-op atomicity" — it's "every individual op is atomic, multi-op is fail-detectable". For our scale (single-user, single-MCP active per data dir under flock) this is sufficient.

`Abort` removes the staging dir without applying anything.

### Disk records vs domain types

Disk yaml is **not** the same shape as the in-memory `domain.*` type. Each entity has a paired record struct in `internal/store/` that mirrors only the fields stored in its yaml frontmatter. Sibling markdown files (`summary.md`, `body.md`, `completion.md`, comment bodies) carry the prose and are loaded separately.

```go
// internal/store/records.go — what's in *.yaml on disk
type ProjectRecord struct {
    ID, Slug, Name, Description string
    CreatedByAgentID            *string   // pointer to *.yaml id; nil if pre-T15
    CreatedAt, UpdatedAt        time.Time
    // NO Summary field — that's summary.md, loaded separately.
}

type TicketRecord struct {
    ID, ProjectID, Title  string
    Column                domain.Column
    PhaseID               *string
    Wave                  int          // soft grouping; 0 = unassigned (omitempty in yaml)
    DependsOn             []string
    ParallelizableWith    []string
    CreatedByAgentID      *string
    CompletedByAgentID    *string
    CompletedAt           *time.Time
    CreatedAt, UpdatedAt  time.Time
    // NO Body, Learnings, etc — those are body.md / completion.md.
}

type CommentRecord struct {
    ID, TicketID                 string
    Kind                         domain.CommentKind
    AuthorAgentID                *string
    FromColumn, ToColumn         *domain.Column
    CreatedAt                    time.Time
    // NO Body — that's the markdown after the frontmatter block.
}

type PhaseRecord struct {
    ID, ProjectID, Slug, Name, Description string
    Number                                 int
    CreatedByAgentID                       *string
    CreatedAt, UpdatedAt                   time.Time
    // NO Summary.
}

type AgentRecord struct {
    // Same fields as domain.Agent — agents have no sidecar files,
    // so the record IS the full domain shape (just stored as yaml).
}
```

The Store package's read functions return records (cheap, no markdown reads). The cache layer (T04) assembles records + sibling files into `domain.*` for handlers to return. Hand-written conversion helpers (`recordToProject(rec ProjectRecord, summary string) *domain.Project`) live in `internal/store/`.

This split is what lets `summary.md` be ≥200 chars without bloating every yaml; lets ticket bodies be markdown files an LLM can `cat`; and keeps integrity-check scans fast (records only).

### Markdown frontmatter codec

Comment files have YAML frontmatter (`---\nkey: value\n---\n`) followed by markdown body. The codec round-trips:

```go
type Frontmatter map[string]any
func WriteMarkdown(path string, fm Frontmatter, body string) error
func ReadMarkdown(path string) (Frontmatter, string, error)
```

### Walk helpers

Walks return **record** types from `internal/store/` — never the hydrated `domain.*` types (those are assembled by the cache layer in T04 from records + sibling markdown files).

- `WalkProjects(func(slug string, rec *ProjectRecord) error)` — iterates `projects/*/project.yaml`.
- `WalkPhases(slug string, func(rec *PhaseRecord) error)` — iterates `projects/<slug>/phases/*/phase.yaml`.
- `WalkTickets(slug string, func(rec *TicketRecord) error)` — iterates both `projects/<slug>/tickets/*/ticket.yaml` and `projects/<slug>/phases/*/tickets/*/ticket.yaml`, ordered by directory name within each tree.
- `WalkComments(ticketDir string, func(rec *CommentRecord, body string) error)` — iterates the comments subdir, ordered by filename (which encodes timestamp); returns the parsed frontmatter record plus the markdown body.
- `WalkAgents(func(rec *AgentRecord) error)` — iterates `agents/*.yaml`.

Each walk is a streaming iterator, not a slurp.

### Active-session uniqueness for agents

`Store.RegisterAgent(rec *AgentRecord)`:
1. `WalkAgents`, collect any non-expired record with the same `Key`. If found, return `domain.ErrAlreadyExists`.
2. Write `agents/<id>.yaml` via `StageOp`.

### Integrity check

`store.Integrity(ctx)`:
- Every `*.yaml` parses without error.
- Every project has `project.yaml` and `summary.md`.
- Every ticket has `ticket.yaml` and `body.md`.
- Every `done` ticket also has `completion.md`.
- Every `created_by` / `completed_by` / `author_id` references an existing `agents/<uuid>.yaml` (or is null).
- `.staging/` is empty (else log a warning naming the residual op-id; do not auto-clean — leave it for human inspection).

Any **structural** failure (unparseable yaml, missing required file) aborts startup with a clear error message naming the path. Soft failures (orphan embedding sidecar, dangling agent ref) log warnings and continue.

### Git auto-commit

```go
func (s *Store) commit(ctx context.Context, paths []string, agent *domain.Agent, summary string) error
```

Implementation:
- Open the repo at the cwd (or wherever `data_dir` lives) via `go-git`.
- If not a repo: warn-log once at startup and disable auto-commit for the process.
- For each call, `wt.Add(relPath)` for each path, then `wt.Commit(summary, &git.CommitOptions{Author: ...})` with `Author.Name = agent.Name`, `Author.Email = agent.Key + "@tickets_please"`, `Author.When = time.Now()`.
- Commit message format:
  ```
  [tickets_please] <verb> <subject> [<agent.name>]

  <summary>
  ```

T07 (move/complete) and T15 (agent register) will call this with verbs like "move ticket", "complete ticket", "register agent".

## Acceptance criteria

- [ ] `store.New` on a fresh data dir creates `agents/`, `projects/`, and `.staging/`.
- [ ] `StageOp` round-trip: writing two files via `BeginOp` + `Write` + `Commit` produces both at their final paths and leaves `.staging/` empty.
- [ ] Killing the process between `Write` and `Commit` leaves `.staging/<op-id>/` populated; integrity at next startup logs the residual op.
- [ ] `WriteMarkdown` + `ReadMarkdown` round-trips frontmatter and body losslessly.
- [ ] `RegisterAgent` rejects a second active session with the same `key` (`domain.ErrAlreadyExists`); accepts after the first record's `expires_at` passes.
- [ ] `WalkComments` returns comments in created-at order regardless of filesystem return order.
- [ ] On a fresh git repo with `auto_commit: true`, calling `op.Commit(ctx, agent, "create project foo")` produces a single commit attributed to `agent.Name`.
- [ ] On a non-git directory, auto-commit logs the warning once and the StageOp still succeeds.
- [ ] Integrity check fails loudly if `summary.md` is missing for a project; it warns (not fails) on a stray `*.embedding.json` without a source.
- [ ] Two simultaneous goroutines calling `WithProjectLock("foo", …)` serialize correctly (verified by a counter test).
- [ ] `WithProjectLock("foo", …)` does NOT block `WithProjectLock("bar", …)`.
- [ ] Lock acquisition timeout fires when configured to a small value and a long-held lock is held by a sibling test.
- [ ] `WatchProject` emits a single coalesced event for a burst of writes (debounce works).

## Notes

See **Data layout**, **Atomicity (the staging + rename pattern)**, **Integrity check (startup)**, and **Auto-commit** in [`../SPEC.md`](../SPEC.md). Keep all paths relative to `Store.Root`; never let absolute paths leak into stored data.

T15 (agent identity) consumes `Store.RegisterAgent` and `Store.WalkAgents` for the session interceptor's lookups. T04+ all sit on top of `StageOp`.
