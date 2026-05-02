---
id: T15
title: Agent identity & in-process middleware
status: TODO
owner: ""
depends_on: [T02, T03]
parallelizable_with: [T08]
wave: 1
files:
  - internal/svc/agents.go
  - internal/svc/middleware.go
  - internal/svc/context.go
estimate: medium
stretch: false
---

# T15 — Agent identity & in-process middleware

> **Wave 1 foundational ticket.** Despite the high number, this lands BEFORE T04–T07 because every mutating service method depends on the session-validating middleware. Schedule alongside T02/T03/T08.

## Scope

Implement `Service.RegisterAgent` / `Heartbeat` / `GetAgent` and the in-process middleware that validates a session ID attached to `context.Context`. No gRPC interceptor — this is pure Go middleware around `svc.Service` methods.

**In:** Agent service methods, in-process middleware, context helpers, integration with the MCP layer's identity flow.

**Out:** No transport layer (the system has none). No metadata-header parsing.

## Files

- `internal/svc/agents.go` — RegisterAgent, Heartbeat, GetAgent
- `internal/svc/middleware.go` — `requireSession` decorator-style helper
- `internal/svc/context.go` — `WithAgent`, `AgentFrom`, `WithSessionID`, `SessionIDFrom`

## Details

### Configuration

Already in T01:
- `agent_session_ttl_minutes` (default 60)
- `agent_session_max_minutes` (default 240)

### `Service.RegisterAgent(ctx, key, name, metadata, requestedTTL)`

1. Validate `key` and `name` non-empty.
2. TTL = `min(requestedTTL_or_default, MaxMinutes)`.
3. Build `Agent`:
   ```go
   &domain.Agent{
       ID: uuid.NewString(),
       Key: key,
       Name: name,
       Metadata: metadata,
       CreatedAt: now,
       ExpiresAt: now.Add(ttl),
       LastSeenAt: now,
   }
   ```
4. `Store.RegisterAgent(agent)` — checks active-key uniqueness via `WalkAgents` filter, writes `agents/<id>.yaml`. Returns `domain.ErrAlreadyExists` on collision.
5. **Skip auto-commit on heartbeats and touch-only writes**; squash registrations into a single commit message: `[tickets_please] register agent <name> [<key-prefix>]`.
6. Return `(agent.ID, agent.ExpiresAt, nil)`.

### `Service.Heartbeat(ctx, sessionID)`

1. `Store.GetAgent(sessionID)`. `ErrNotFound` if missing.
2. If `ExpiresAt < now` → `ErrUnauthenticated`.
3. Update `LastSeenAt = now`, rewrite via `StageOp` (no auto-commit).
4. Return `(agent.ExpiresAt, nil)` — TTL **not** extended.

### `Service.GetAgent(ctx, id)`

Read-only. Returns the full `Agent`.

### Context helpers (`internal/svc/context.go`)

```go
type ctxKey int
const (
    keySessionID ctxKey = iota
    keyAgent
)

func WithSessionID(ctx context.Context, id string) context.Context
func SessionIDFrom(ctx context.Context) (string, bool)
func WithAgent(ctx context.Context, a *domain.Agent) context.Context
func AgentFrom(ctx context.Context) (*domain.Agent, bool)
```

The MCP layer (T12) calls `WithSessionID(ctx, id)` before invoking each `svc.Service` method. The middleware (below) reads the session id, validates, attaches the full `*Agent`.

### Middleware (`internal/svc/middleware.go`)

Each mutating method on `Service` starts with:

```go
func (s *Service) CreateTicket(ctx context.Context, in domain.CreateTicketInput) (*domain.Ticket, error) {
    ctx, agent, err := s.requireSession(ctx)
    if err != nil { return nil, err }
    // ... rest of handler reads agent via AgentFrom(ctx) ...
}
```

Where `requireSession` does:

```go
func (s *Service) requireSession(ctx context.Context) (context.Context, *domain.Agent, error) {
    id, ok := SessionIDFrom(ctx)
    if !ok { return ctx, nil, fmt.Errorf("%w: register an agent first", domain.ErrUnauthenticated) }
    a, err := s.Store.GetAgent(id)
    if err != nil {
        if errors.Is(err, domain.ErrNotFound) {
            return ctx, nil, fmt.Errorf("%w: unknown session", domain.ErrUnauthenticated)
        }
        return ctx, nil, err
    }
    if a.ExpiresAt.Before(time.Now()) {
        return ctx, nil, fmt.Errorf("%w: session expired; re-register", domain.ErrUnauthenticated)
    }
    s.touchAgentDebounced(a.ID)  // best-effort, non-blocking
    return WithAgent(ctx, a), a, nil
}
```

`touchAgentDebounced` updates `LastSeenAt` via `StageOp` but rate-limited (one write per agent per minute) so we don't generate audit-trail noise.

Read methods (`Get*`, `List*`, `Search*`) **do not** call `requireSession`. They run regardless.

### Integration with T04+ handlers

T04, T05, T06, T07, T16 each begin every mutating handler with `s.requireSession(ctx)`. Reads skip it. T12's MCP tools call `WithSessionID` before invoking `svc` methods.

T04+ may also reach for `domain.AgentFrom(ctx)` directly to populate `created_by` / `completed_by` / `author_id` on the rows they write.

## Acceptance criteria

- [ ] `Service.RegisterAgent` writes `agents/<uuid>.yaml` with the expected fields.
- [ ] Two `RegisterAgent` calls with the same `key` while the first is active → `ErrAlreadyExists`.
- [ ] After the first session's `ExpiresAt` passes, a fresh `RegisterAgent` with the same key succeeds.
- [ ] Calling a mutating method with `WithSessionID(ctx, "")` → `ErrUnauthenticated`.
- [ ] Calling with a stale session id → `ErrUnauthenticated`.
- [ ] Calling a read method without a session id succeeds.
- [ ] `LastSeenAt` updates on a successful mutating call (debounced; consecutive calls within a minute do not all rewrite).
- [ ] `Heartbeat` updates `LastSeenAt` but does NOT extend `ExpiresAt`.
- [ ] `AgentFrom(ctx)` returns the correct agent inside a handler.

## Notes

See **Agent identity & sessions** in [`../SPEC.md`](../SPEC.md). Pure in-process — no gRPC interceptor, no metadata header. T12 (MCP) sets up the context; T04+ read it.
