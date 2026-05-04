## Phase: MCP auth hardening

External bug report (Codex, 2026-05-04): a `complete_ticket` call returned the bare text `unauthenticated; re-registering...` and **did not** complete the ticket. Retrying produced the same string. `who_am_i` reported `registered: true` despite the underlying svc-layer agent record being expired. Manually calling `register_agent` minted a fresh session and the same `complete_ticket` call then succeeded.

## Root cause

The bug lives entirely in `internal/mcptools` — the svc layer's `requireSession` ([middleware.go:24-42](internal/svc/middleware.go#L24-L42)) does the right thing, but the wrapper above it doesn't:

1. **`callWithRetry` ([tools.go:301](internal/mcptools/tools.go#L301)) doesn't retry.** It checks Registry presence, threads the cached `AgentID` into context, and calls fn once. When fn returns `ErrUnauthenticated` because the underlying `AgentRecord.ExpiresAt` has passed, it surfaces the error verbatim — no refresh, no retry.
2. **`formatError` ([format.go:251-252](internal/mcptools/format.go#L251-L252)) lies.** Maps `ErrUnauthenticated` to the literal string `"unauthenticated; re-registering..."` even though nothing re-registers.
3. **`who_am_i` ([tools.go:1111](internal/mcptools/tools.go#L1111)) hides expiry.** Returns `registered: true` based purely on Registry presence; no `expired` field, so the LLM has to parse `expires_at` itself to detect the state.

## Why auto-refresh is feasible

The Session struct already caches everything needed — `AgentKey`, `AgentName`, `Metadata`, `ProjectSlug`, `ProjectPath` — so the wrapper can call `svc.RegisterAgent` again without any client interaction. `AgentStore.RegisterAgent` ([store/agents.go:124-162](internal/store/agents.go#L124-L162)) allows re-registering with the same key once the prior record's `ExpiresAt` has passed (verified by [store_test.go:329](internal/store/store_test.go#L329); also called out in past learning on T002 web-foundation: "AgentStore.RegisterAgent only returns ErrAlreadyExists for non-expired sessions with the same key"). And because `requireSession` runs **before** any state change in mutating svc methods, an `ErrUnauthenticated` return guarantees the operation was a no-op — single-shot retry is safe.

## Tickets in this phase

```
1 Auto-refresh expired sessions in callWithRetry + honest error string
2 Surface expiry on who_am_i (additive `expired` field)
```

T001 carries the load-bearing fix (auto-retry + formatError). T002 is observability: an `expired` boolean on `who_am_i` so clients can detect the state without parsing timestamps. Independent — could parallelise, but T001 is the meatier change.

## Out of scope

- Project-mount refresh (the in-memory mount survives across the in-process retry; no need to re-mount).
- Stdio bootstrap changes — `cmd/tickets_please/main.go` pre-registers a session at startup; auto-refresh applies the same way.
- Changes to the svc layer or `AgentStore` — both already correct.

## Reference

- Plan: `/home/dan/.claude/plans/yep-the-concrete-issue-serialized-map.md`
- Critical files: `internal/mcptools/tools.go`, `internal/mcptools/format.go`, `internal/mcptools/identity.go`, `internal/svc/middleware.go`, `internal/svc/agents.go`.
