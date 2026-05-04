## Goal

When a mutating MCP tool call hits `ErrUnauthenticated` because the calling session has expired in the svc layer's `AgentStore`, transparently refresh the session using the cached identity and retry the call once. If refresh fails, return a structured, accurate error — never the misleading `"unauthenticated; re-registering..."` string the wrapper currently emits without actually re-registering.

## Background

External bug report from Codex: `complete_ticket` returned the literal text `unauthenticated; re-registering...` and did not complete the ticket. Retrying gave the same string. Manually calling `register_agent` minted a fresh session and the same `complete_ticket` call then succeeded. So:

- The svc layer's `requireSession` ([internal/svc/middleware.go:36-37](internal/svc/middleware.go#L36-L37)) correctly rejects expired records: `if rec.ExpiresAt.Before(time.Now()) { return ... %w: session expired; re-register }`.
- The MCP wrapper `callWithRetry` ([internal/mcptools/tools.go:301-310](internal/mcptools/tools.go#L301-L310)) only checks Registry presence, threads the AgentID, and calls fn once. The Registry entry survives even after the underlying AgentRecord expires.
- `formatError` ([internal/mcptools/format.go:251-252](internal/mcptools/format.go#L251-L252)) maps `ErrUnauthenticated` to a fixed string promising re-registration that never happens.

`requireSession` runs **before** any state change in mutating svc methods, so `ErrUnauthenticated` returns guarantee a no-op — single-shot retry is safe. The Session already caches `AgentKey`, `AgentName`, `Metadata`, `ProjectSlug`, `ProjectPath`, so the wrapper has everything it needs to mint a fresh session without client interaction. `AgentStore.RegisterAgent` ([internal/store/agents.go:124-162](internal/store/agents.go#L124-L162)) accepts re-registration with the same key once the existing record has expired (covered by [store/store_test.go:329](internal/store/store_test.go#L329)).

## Acceptance criteria

1. **Single-shot auto-refresh on expiry.** When `fn` inside `callWithRetry` returns `ErrUnauthenticated`, attempt one refresh: call `t.svc.RegisterAgent(ctx, prev.AgentKey, prev.AgentName, prev.Metadata, 0)`, build a new `Session` carrying the new `AgentID`/`ExpiresAt` and the cached `ProjectSlug`/`ProjectPath`, register it under the same MCP session ID (last-write-wins), then re-invoke `fn` with `svc.WithSessionID(ctx, newSess.AgentID)`. Bounded to one retry — a second `ErrUnauthenticated` returns verbatim, no looping.
2. **Honest fallback error.** Replace the `formatError` `ErrUnauthenticated` branch (`format.go:251-252`) with `"unauthenticated: " + stripSentinel(err, domain.ErrUnauthenticated)` so the message reflects the wrapped svc error instead of lying about re-registration. The user-visible string only appears when auto-refresh **also** failed.
3. **Refresh failure surfaces clearly.** If the refresh `RegisterAgent` call returns an error, wrap it as `fmt.Errorf("%w: session expired and auto-refresh failed (%v); call register_agent", domain.ErrUnauthenticated, refreshErr)` so the existing format-error branch surfaces a clean message that includes the underlying reason.
4. **Unregistered-session path unchanged.** When no Registry entry exists for the MCP session, `callWithRetry` keeps returning the existing `"no agent registered for session %q; call register_agent first"` error — auto-refresh only applies when there's a cached identity to reuse.
5. **Logging.** Emit one info-level log line per successful auto-refresh: session ID, agent key, and the new `expires_at`. Helpful for debugging recurring expiry issues without spamming.

## Implementation sketch

In `internal/mcptools/tools.go`:

```go
func (t *Tools) callWithRetry(ctx context.Context, fn func(ctx context.Context) error) error {
    sessionID := t.sessionIDFromContext(ctx)
    sess, ok := t.registry.Get(sessionID)
    if !ok {
        return fmt.Errorf("%w: no agent registered for session %q; call register_agent first",
            domain.ErrUnauthenticated, sessionID)
    }
    err := fn(svc.WithSessionID(ctx, sess.AgentID))
    if err == nil || !errors.Is(err, domain.ErrUnauthenticated) {
        return err
    }
    newSess, refreshErr := t.refreshSession(ctx, sessionID, sess)
    if refreshErr != nil {
        return fmt.Errorf("%w: session expired and auto-refresh failed (%v); call register_agent",
            domain.ErrUnauthenticated, refreshErr)
    }
    return fn(svc.WithSessionID(ctx, newSess.AgentID))
}

func (t *Tools) refreshSession(ctx context.Context, sessionID string, prev *Session) (*Session, error) {
    agentID, expiresAt, err := t.svc.RegisterAgent(ctx, prev.AgentKey, prev.AgentName, prev.Metadata, 0)
    if err != nil {
        return nil, err
    }
    next := &Session{
        AgentID:     agentID,
        AgentKey:    prev.AgentKey,
        AgentName:   prev.AgentName,
        Metadata:    prev.Metadata,
        ProjectSlug: prev.ProjectSlug,
        ProjectPath: prev.ProjectPath,
        ExpiresAt:   expiresAt,
    }
    if err := t.registry.Register(sessionID, next); err != nil {
        return nil, err
    }
    t.logger.Info("auto-refreshed expired mcp session",
        "session_id", sessionID, "agent_key", prev.AgentKey, "expires_at", expiresAt)
    return next, nil
}
```

In `internal/mcptools/format.go`:

```go
case errors.Is(err, domain.ErrUnauthenticated):
    return "unauthenticated: " + stripSentinel(err, domain.ErrUnauthenticated)
```

## Tests

- **Update** `tools_test.go:350` (the format-error case in `TestFormatError` or equivalent) to assert the new structured form, e.g. expected `"unauthenticated: session expired"` for the existing test fixture.
- **Add** an end-to-end refresh test in `register_agent_test.go`, building on the `freshToolsForRegister` fixture:
  1. Call `handleRegisterAgent` to mint an initial session.
  2. Read the resulting `AgentRecord`, force `ExpiresAt = now-1m`, write it back via `s.AgentStore.WriteAgentRecord`.
  3. Capture the original `AgentID`/`ExpiresAt` from the registry.
  4. Invoke a mutating handler (e.g. `handleCreateTicket`) and assert it succeeded (no error, ticket returned).
  5. Re-read the registry's session: assert `AgentID` differs and `ExpiresAt > time.Now()`.
- **Add** a refresh-failure test that primes the AgentStore so `RegisterAgent` will fail (e.g. an empty agent key by mutating the cached Session in-place before the call), invokes a handler, and asserts the structured error is returned and contains the underlying reason.

## Out of scope

- Project-mount refresh — the in-memory mount survives across the in-process retry; auto-refresh skips that step deliberately.
- The `expired` field on `who_am_i` — covered by ticket 2 in this phase.
- Changes to svc layer / AgentStore behavior.
