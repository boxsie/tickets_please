---
id: T13
title: Integration tests
status: TODO
owner: ""
depends_on: [T07, T11, T15]
parallelizable_with: [T12, T14]
wave: 6
files:
  - internal/svc/svc_test.go
  - internal/svc/tickets_test.go
  - internal/svc/comments_test.go
  - internal/svc/search_test.go
  - internal/svc/agents_test.go
  - internal/svc/phases_test.go
  - internal/embed/fake.go
estimate: medium
stretch: true
---

# T13 — Integration tests *(stretch)*

## Scope

Lock in the load-bearing invariants with end-to-end tests against the real filesystem store, using `t.TempDir()` as the data dir. No Docker, no DB containers, no MCP transport — tests call `svc.Service` methods directly.

**In:** Test harness, svc-level tests, embedding-pipeline tests against a fake provider, concurrency tests for the lock + fsnotify model.

**Out:** No load tests, no chaos. No MCP-transport tests (that's T12's job; the wire layer is so thin it's not worth integration-testing).

## Files

- `internal/svc/svc_test.go` *(harness)*
- `internal/svc/tickets_test.go`
- `internal/svc/comments_test.go`
- `internal/svc/search_test.go`
- `internal/svc/agents_test.go`
- `internal/svc/phases_test.go`
- `internal/embed/fake.go` *(test-only `Provider` returning deterministic vectors based on input hash)*

## Details

### Harness

`svc_test.go` exposes `setupTestService(t)`:
1. `t.TempDir()` for the data dir.
2. Build a `Store` rooted there with `auto_commit: false` (tests don't need git noise).
3. Build a fake embedding provider returning deterministic 768-float vectors derived from a SHA-256 of the input.
4. Build `svc.Service` via `svc.New(cfg)`. Register a test agent via `Service.RegisterAgent` and capture its session id.
5. Return `(svc *Service, agentSessionID string)`.
6. `t.Cleanup` cancels the worker context and clears the temp dir.

Tests call methods directly: `ctx := svc.WithSessionID(context.Background(), agentSessionID); _, err := s.CreateTicket(ctx, in)`. No transport, no client, no port.

### Tests to cover

- **Move requires comment**: `MoveTicket` with empty comment → `InvalidArgument`. Confirm `ticket.yaml` unchanged on disk and no new comment file appeared.
- **Move to DONE rejected**: `target_column = COLUMN_DONE` → `InvalidArgument` mentioning `CompleteTicket`.
- **Move from DONE rejected**: complete a ticket, then `MoveTicket` → `FailedPrecondition`.
- **Move atomicity**: arrange a write failure mid-`StageOp.Commit` (inject via a Store hook or a temporary read-only filesystem trick) and confirm the ticket is unchanged AND the staging dir is left for inspection.
- **Complete requires substantive fields**: `learnings = "."` → `InvalidArgument`. Each of the three fields tested independently.
- **Complete idempotency**: complete a ticket; complete it again → `FailedPrecondition`.
- **Comments are immutable**: there is no `UpdateComment` or `DeleteComment` method on `svc.Service` (compile-time check via reflection on the type).
- **Embedding round-trip**: create a ticket, wait for the worker to embed it, `SearchTickets("<paraphrase>")` returns it. Use the fake provider so vectors are deterministic.
- **SearchLearnings filters to done**: complete ticket A, leave ticket B unfinished. `SearchLearnings` returns only A.
- **Backfill**: write a ticket via the `Store` directly (bypassing handler enqueue), then call `LoadProject`, then confirm `body.embedding.json` is created within ~5s.
- **Project cache eviction**: configure `project_idle_minutes=0` and confirm a load → access → wait → next access cycle reloads cleanly.
- **Agent session expiry**: configure `agent_session_ttl_minutes=0` (or use a clock-skewed test) and confirm a mutating call returns `domain.ErrUnauthenticated`.
- **Active-key uniqueness**: `RegisterAgent` twice with same key while the first is active → `domain.ErrAlreadyExists`.
- **Concurrency — per-project lock**: two goroutines call `MoveTicket` on the same ticket simultaneously; the slower one observes the post-state from the faster one (no lost write).
- **Concurrency — different projects don't block**: goroutine A holds a long write on project foo; goroutine B writes to project bar without waiting.
- **fsnotify cross-process invalidation**: simulate by writing to a project file out-of-band (bypassing svc), then call `GetTicket` and confirm the cache reloaded.
- **Auto-commit (in a real git repo subtest)**: init a git repo at `t.TempDir()`, set `auto_commit: true`, perform create/move/complete, confirm three commits in `git log` with the agent as author.

### Performance check (optional)

A `TestSearchPerf` running 10k random vectors confirming `Search` returns top-10 in <50ms on the test machine. Useful as a regression marker if we ever switch indexes.

## Acceptance criteria

- [ ] `go test ./...` passes.
- [ ] Each invariant has a named test that fails when the invariant is broken (verify by temporarily breaking it).
- [ ] Tests run in <30s on a warm `go test` cache; no Docker, no external services.
- [ ] No test depends on Ollama being running (use the fake provider).
- [ ] `-race` runs clean.

## Notes

The whole reason filesystem storage is appealing is `t.TempDir()` works. No testcontainers needed, no fixtures dir needed — every test gets a clean world for free.
