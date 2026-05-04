## Goal

Add an `expired` boolean to `who_am_i`'s output so MCP clients can detect a stale session without parsing the `expires_at` timestamp. Today `who_am_i` ([internal/mcptools/tools.go:1111-1147](internal/mcptools/tools.go#L1111-L1147)) returns `registered: true` whenever the in-memory Registry has an entry, even when the underlying svc-layer `AgentRecord.ExpiresAt` has passed — exactly the contradiction the Codex bug report flagged.

## Acceptance criteria

1. **Additive field.** When the session is registered, the `who_am_i` payload includes `expired: <bool>` derived from `time.Now().After(sess.ExpiresAt)`. The existing `registered` semantic ("Registry entry exists") stays the same so existing consumers don't break.
2. **Unregistered case unchanged.** When the session has no Registry entry, the response still includes `registered: false` and the existing nil fields (`key`, `name`, `expires_at` all `nil`); no `expired` key is added (there is no expiry to report).
3. **Test coverage.** Add a test that primes a registered Session with `ExpiresAt = time.Now().Add(-1*time.Minute)` and asserts the response payload contains `"expired": true`. Add a sibling assertion that a fresh Session with `ExpiresAt` in the future yields `"expired": false`.

## Implementation sketch

Inside `handleWhoAmI` ([internal/mcptools/tools.go:1123-1130](internal/mcptools/tools.go#L1123-L1130)):

```go
out := map[string]any{
    "session_id": sessionID,
    "registered": true,
    "key":        sess.AgentKey,
    "name":       sess.AgentName,
    "agent_id":   sess.AgentID,
    "expires_at": formatTime(sess.ExpiresAt),
    "expired":    !sess.ExpiresAt.IsZero() && time.Now().After(sess.ExpiresAt),
}
```

The `IsZero()` guard handles the bootstrap stdio session, which is registered with no AgentID and no ExpiresAt before its first svc call (see `DefaultStdioSession` at [identity.go:99-119](internal/mcptools/identity.go#L99-L119)) — those entries should report `expired: false` until they're populated.

## Tests

In `tools_test.go` (or `register_agent_test.go` if more convenient):

- Reuse the existing `who_am_i` test pattern. Pre-register a Session with ExpiresAt in the past, call `handleWhoAmI`, decode the JSON, assert `"expired": true`.
- Pre-register one with ExpiresAt in the future, assert `"expired": false`.
- Optionally: with no Registry entry, assert `"expired"` is **absent** (or, equivalently, the rendered payload still matches the existing un-registered shape).

## Notes

This ticket is independent of T001 in the same phase but complementary — together they make the auth state visible (T002) and self-healing (T001).
