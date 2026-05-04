## Testing evidence
Added two new tests in `internal/mcptools/register_agent_test.go` exercising the failure mode from the Codex bug report end-to-end against a real `svc.Service`:

1. `TestCallWithRetry_AutoRefreshOnExpiry` — registers an agent, force-rewrites its `AgentRecord.ExpiresAt` to `now-1m` via `s.AgentStore.WriteAgentRecord`, calls `handleCreateTicket`, asserts the call **succeeds** silently, and asserts the registry's Session now carries a different `AgentID` and a fresh `ExpiresAt`, while keeping the original `AgentKey` and project binding.
2. `TestCallWithRetry_RefreshFailureSurfaces` — same setup, but additionally sabotages the cached Session's `AgentKey` to `""` so `svc.RegisterAgent` returns `ErrInvalidArgument` from inside `refreshSession`. Asserts the response is an MCP error result whose message contains both `"unauthenticated:"` and `"auto-refresh failed"`, and does **not** contain the legacy `"re-registering..."` string.

Updated the existing `TestFormatError` case (`tools_test.go:350`) from the lying string to the structured form `"unauthenticated: session expired"`.

Verbose run output:
```
=== RUN   TestCallWithRetry_AutoRefreshOnExpiry
2026/05/04 12:51:31 INFO auto-refreshed expired mcp session session_id=stdio agent_key=claude_code:41999eb173b118c1 expires_at=2026-05-04T12:51:31Z
--- PASS: TestCallWithRetry_AutoRefreshOnExpiry (0.00s)
=== RUN   TestCallWithRetry_RefreshFailureSurfaces
--- PASS: TestCallWithRetry_RefreshFailureSurfaces (0.00s)
=== RUN   TestFormatError
--- PASS: TestFormatError (0.00s)
```

Full suite (`go test ./...`) is green: cmd, cache, embed, mcptools, store, svc, vecindex, web, worker. `go vet ./...` and `gofmt -l internal/mcptools/` both clean.

## Work summary
**internal/mcptools/format.go** — replaced the misleading `"unauthenticated; re-registering..."` string in the `formatError` `ErrUnauthenticated` branch with the structured form `"unauthenticated: " + stripSentinel(err, domain.ErrUnauthenticated)`, matching the prefix style used for every other domain sentinel. The user-visible string now only surfaces when auto-refresh has also failed, so it's load-bearing rather than misleading.

**internal/mcptools/tools.go** — extended `callWithRetry` with a single-shot auto-refresh path. When `fn` returns `ErrUnauthenticated`, the wrapper now invokes a new `refreshSession` helper that:

- calls `t.svc.RegisterAgent(ctx, prev.AgentKey, prev.AgentName, prev.Metadata, 0)` to mint a fresh svc-layer agent under the cached identity,
- builds a new `Session` carrying the new `AgentID`/`ExpiresAt` and the cached `ProjectSlug`/`ProjectPath`,
- writes it back into the registry under the same MCP session id (last-write-wins on `Registry.Register`),
- emits one info-level log line (`session_id`, `agent_key`, `expires_at`).

`callWithRetry` then re-invokes `fn` once with the new `AgentID`. A second `ErrUnauthenticated` returns verbatim — bounded to one retry, no looping. If the refresh itself errors, the wrapper returns `fmt.Errorf("%w: session expired and auto-refresh failed (%v); call register_agent", domain.ErrUnauthenticated, refreshErr)` so the new `formatError` branch surfaces a clean message including the underlying reason. The unregistered-session path (no Registry entry) keeps its existing error verbatim — auto-refresh only runs when there's a cached identity to reuse. Added `"errors"` to the import block so `errors.Is` is callable.

**internal/mcptools/tools_test.go** — updated the `TestFormatError` `"unauth"` case expectation from the legacy string to `"unauthenticated: session expired"`.

**internal/mcptools/register_agent_test.go** — added two tests built on `freshToolsForRegister`: `TestCallWithRetry_AutoRefreshOnExpiry` (happy-path silent refresh) and `TestCallWithRetry_RefreshFailureSurfaces` (sabotaged refresh returns the structured fallback message).

No svc, store, or domain code touched — the bug lived entirely in the MCP wrapper layer; both lower layers were already correct.

## Learnings
**The Session struct already had everything needed for transparent refresh.** `AgentKey`, `AgentName`, `Metadata`, `ProjectSlug`, `ProjectPath` all live on the cached Session — `refreshSession` is a pure in-process operation requiring zero client interaction and zero project re-mounting. The mount lives in svc memory and survives the retry. This was the unlock that made auto-refresh feasible without a protocol change.

**Safety-of-retry argument runs through requireSession.** `internal/svc/middleware.go:24-42` runs at the top of every mutating svc method **before** any state change. So an `ErrUnauthenticated` return guarantees the caller's operation was a no-op, which is the only reason single-shot retry is safe. If a future svc method ever does work before calling requireSession, this assumption breaks; worth a comment on the auto-retry path if that risk grows.

**`AgentStore.RegisterAgent` permits same-key re-registration once the prior record has expired.** Confirmed by [store/store_test.go:329](internal/store/store_test.go#L329) and reinforced by a prior learning on web-foundation T002 (the cookie/session flow uses the same property). The `WalkAgents` active-key uniqueness check uses `existing.ExpiresAt.After(now)` — a force-expired record (expiry in the past) does not register as a conflict, so `refreshSession` lands cleanly. The old `AgentRecord` yaml stays on disk harmless.

**`ReadAgent` always reads fresh from disk.** Verified by inspection of `internal/svc/middleware.go:29` — there is no in-memory caching layer above the file read, so writing a new `ExpiresAt` via `WriteAgentRecord` immediately changes the next `requireSession` outcome. This made the test setup trivial: just rewrite the yaml on disk and the next mutator call sees the expired record.

**Testing the auto-refresh path needed a real `svc.Service`, not the simpler `newTestRegistry()` fixture.** `tools_test.go`'s pre-loaded "stdio" session is bound to `svc=nil`, which is fine for handler-shape tests but breaks the moment a handler actually invokes svc. `freshToolsForRegister` ([register_agent_test.go:22](internal/mcptools/register_agent_test.go#L22)) wires a real `svc.Service` + `AgentStore` rooted in a tempdir + a `.tickets_please/project.yaml`, which is the right fixture for any test that exercises the auth-middleware-to-svc round trip. Future expiry/auth tests should reuse it.

**Do not store an `expired` boolean on the cached Session.** I considered caching `expired` derived from a clock at Session-creation time, but that field would silently lie the moment time passes. The honest pattern is to compute it on read (which the sister ticket T002 will do for `who_am_i`) or to ask the persistent `AgentRecord` (which is what `requireSession` already does). The cached Session is a pointer to identity, not a cached auth-state — it should never claim authenticated/not by itself.

**`mcp.CallToolRequest{}` with `req.Params.Arguments = map[string]any{...}` is the canonical handler-test invocation pattern** in this package — no need to round-trip through the mcp-go transport. Used by `callRegister` and reused for both new tests.

**Logging at info level was the right pitch for auto-refresh.** Per-mutation-call refresh would be too noisy for debug; per-refresh is bounded by the session TTL (default 60 minutes) — at most one info line per agent per hour even under load. The single line carries `session_id`, `agent_key`, and the new `expires_at` — enough to correlate with downstream tool calls without needing to grep multi-line output.

**Auto-refresh deliberately skips re-mounting the project.** `handleRegisterAgent` calls `t.svc.RegisterProjectMount` before `t.svc.RegisterAgent`, but the mount is in-process state that survives the in-process retry. Re-mounting on every refresh would be wasted I/O and could surface mount errors that didn't exist when the original session was healthy. If the mount ever does drop (e.g. process restart), every Session in the registry is lost too, so the no-Registry-entry path handles that case.
