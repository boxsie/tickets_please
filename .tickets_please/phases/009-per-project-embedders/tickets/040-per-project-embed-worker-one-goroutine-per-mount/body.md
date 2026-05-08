## Goal

`worker.Worker` is currently a single goroutine reading one queue and using one `w.provider` (`internal/worker/embed.go`). Per-mount providers mean per-mount workers.

## Scope

- `internal/worker/embed.go` — `worker.New(...)` accepts a `provider embed.Provider` and the four target `*vecindex.Index` pointers (or an `IndexHost interface{ IndexFor(JobKind) *vecindex.Index }` — pick whichever is cleaner once you're in the file). One goroutine per mount; `indexFor` becomes a tiny struct lookup, not a kind-to-global mapping.
- Lifecycle: `Worker.Stop()` (or context-cancel) wired into the existing mount eviction path. The DeleteTicket learnings flag that `Worker.Flush(ctx)` before any tree-removal is load-bearing — keep it.
- Mount construction in W2-T1 calls `worker.New(...)` and stashes the worker on `ProjectMount`. Enqueue paths in `internal/svc/hydrate.go:162` (`upsertOrEnqueue`) and elsewhere now route through `mount.Worker.Enqueue(...)` instead of a single `s.Worker`.
- `s.Worker` field on Service: drop it once all callers route through mounts. (Keep a fallback for stdio single-project? Probably not needed — eager-mount in `NewWithEmbed` covers the stdio path; verify with the existing stdio integration test.)

## Tests

- Existing worker tests (`internal/worker/embed_test.go`) — rework against the new constructor signature. Two-mount integration: enqueue jobs to mount A, assert mount B's indexes stay empty.
- Concurrency test: run two mounts side-by-side with different probed dims; confirm no cross-talk.

## Done when

- `make build` + `go test ./...` green.
- `grep -n "s.Worker" internal/svc/` returns nothing — all enqueues go through a mount.
