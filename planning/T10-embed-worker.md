---
id: T10
title: Embedding worker (JSON sidecars + vec index)
status: TODO
owner: ""
depends_on: [T07, T08, T09]
parallelizable_with: []
wave: 3
files:
  - internal/worker/embed.go
  - internal/svc/projects.go
  - internal/svc/tickets.go
  - internal/svc/comments.go
  - cmd/tickets_please/main.go
estimate: medium
stretch: false
---

# T10 — Embedding worker (JSON sidecars + vec index)

## Scope

The async embedding pipeline. Server handlers fire-and-forget enqueue jobs after their store writes commit. A goroutine drains the channel, calls the embedding provider, writes the `*.embedding.json` sidecar, and `Upsert`s into the right `vecindex.Index`. Backfill on project load picks up missing sidecars.

**In:** Worker package, enqueue helpers in handlers, backfill walk on project load, lifecycle wiring in `cmd/tickets_please/main.go`.

**Out:** No search SQL or RPCs — T11 reads what this writes.

## Files

- `internal/worker/embed.go`
- Patches to `internal/svc/{projects,tickets,comments}.go` (markers like `// T10: enqueue embed job here` left by T04/T05/T06/T07)
- `cmd/tickets_please/main.go` — wire startup, dim check, backfill, graceful shutdown

## Details

### Worker shape

```go
type JobKind int
const (
    JobUnspecified JobKind = iota
    JobProjectSummary
    JobTicketBody
    JobTicketLearnings
    JobComment
)

type Job struct {
    Kind        JobKind
    SourcePath  string  // path to source markdown (body.md / completion.md / summary.md / comment file)
    SidecarPath string  // path to write the JSON sidecar
    EntryID     string  // id used for vecindex Upsert (project_id / ticket_id / comment_id)
    Owner       string  // project slug
    Text        string  // text to embed (caller has already extracted, e.g. learnings section only)
    TargetIndex *vecindex.Index  // which index to Upsert into (per-project or resident)
}

type Worker struct {
    queue    chan Job
    provider embed.Provider
    log      *slog.Logger
}

func New(provider embed.Provider, bufferSize int, log *slog.Logger) *Worker
func (w *Worker) Enqueue(j Job)              // non-blocking; warn-log + drop if full
func (w *Worker) Run(ctx context.Context)    // blocks until ctx done
```

Buffer size: 256.

### When handlers enqueue

- `CreateProject` / `UpdateProject(summary)` → `JobProjectSummary` with text = `summary.md` content; target = resident `projects_summary_index`.
- `CreateTicket` / `UpdateTicket` (when title or body changes) → `JobTicketBody` with text = `title + "\n\n" + body`; target = the project's per-project index.
- `CompleteTicket` → `JobTicketLearnings` with text = the Learnings section of `completion.md`; target = resident `learnings_index`.
- `CreateComment` → `JobComment`. Same for system comments inserted by `MoveTicket` and `CompleteTicket`. Target = the project's per-project index.

Enqueue **after** the StageOp commits, never inside it. If commit fails, no job.

### Worker loop

```go
for j := range w.queue {
    select { case <-ctx.Done(): return; default: }
    vec, err := w.provider.Embed(ctx, j.Text)
    if err != nil { w.log.Warn("embed failed", "err", err, "path", j.SourcePath); continue }
    if err := vecindex.WriteSidecar(j.SidecarPath, vec); err != nil { ... ; continue }
    j.TargetIndex.Upsert(vecindex.Entry{ID: j.EntryID, Kind: kindFor(j.Kind), Owner: j.Owner, Vec: vec})
}
```

Per-job errors are logged and skipped — not fatal. Backfill picks them up next time.

### Backfill

Two scopes:

1. **Resident indexes (boot)**:
   - For each `projects/<slug>/summary.md` lacking `summary.embedding.json` → enqueue.
   - For each `projects/<slug>/tickets/<NNN>-…/completion.md` lacking `learnings.embedding.json` → enqueue.

2. **Per-project (on `LoadProject`)**:
   - For each `tickets/<NNN>-…/body.md` lacking `body.embedding.json` → enqueue.
   - For each `tickets/<NNN>-…/comments/*.md` lacking the sibling `*.embedding.json` → enqueue.

The walk happens synchronously *before* `LoadProject` returns so the `LoadedProject` reports a consistent ticket/comment count, but the actual embeds run async — the project is loaded with a partial vec index that fills in over the next few seconds.

### Lifecycle in main

1. Build provider via `embed.New(cfg)`.
2. **Dim check**: if `provider.Dim() != 768` → log error and `os.Exit(1)`. The disk format and the in-memory format both assume 768 for v1.
3. Build worker, start `worker.Run(ctx)` in a goroutine.
4. Run resident-index backfill in a goroutine (don't block startup).
5. On shutdown signal, cancel the context; `Run` drains in-flight jobs.

## Acceptance criteria

- [ ] Create a project with a summary. Within ~5s, `summary.embedding.json` exists next to `summary.md` and `projects_summary_index` contains it.
- [ ] Update a ticket's title. The body sidecar is regenerated; the old vec is replaced in the per-project index.
- [ ] Complete a ticket. `learnings.embedding.json` is written and `learnings_index` includes it.
- [ ] Add a user comment. Its sibling `*.embedding.json` is written and the per-project index has it.
- [ ] Stop the server with embeds in flight, restart. Backfill catches up the missing sidecars.
- [ ] `find .tickets_please -name '*.embedding.json' -delete` followed by a restart re-creates them all.
- [ ] Starting with `EMBED_PROVIDER=openai` (Dim != 768) refuses to start with a clear error.

## Notes

See **Embedding pipeline** and **Vector search index** in [`../SPEC.md`](../SPEC.md). T07 left `// T10: enqueue embed job here` markers in handlers; grep for them and patch. Always enqueue **after** `StageOp.Commit` succeeds.
