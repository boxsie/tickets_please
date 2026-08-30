---
id: T15
title: Service skeleton + agent identity + in-process middleware
status: DONE
owner: subagent-T15
depends_on: [T02, T03]
parallelizable_with: [T09]
wave: 2
files:
  - internal/svc/service.go
  - internal/svc/agents.go
  - internal/svc/middleware.go
  - internal/svc/context.go
estimate: medium
stretch: false
---

# T15 — Service skeleton + agent identity + in-process middleware

> **Wave 2.** Depends on T02 (Store helpers) and T03 (domain types). Lands BEFORE T04 — T04 *extends* the `Service` struct this ticket creates.

## Scope

Define the `Service` struct (the in-process API surface), build its constructor, implement `RegisterAgent` / `Heartbeat` / `GetAgent`, and ship the session-validating middleware that every later ticket's mutating method calls.

**In:** `Service` struct + `New`, agent methods, middleware, context helpers.

**Out:** Project/ticket/comment/search methods (T04+ add those). No project cache (T04). No embedding worker (T10). No vector indexes (T09). T15's `Service` declares only the foundational fields; later tickets *append* fields they own.

## Files

- `internal/svc/service.go` — **the canonical `Service` struct** + `New(cfg)` constructor (this ticket owns the file; later tickets add fields and constructor wiring)
- `internal/svc/agents.go` — RegisterAgent, Heartbeat, GetAgent
- `internal/svc/middleware.go` — `requireSession` decorator-style helper
- `internal/svc/context.go` — `WithAgent`, `AgentFrom`, `WithSessionID`, `SessionIDFrom`

## Details

### `Service` struct (canonical, in `service.go`)

T15 declares the minimal foundational shape. Later tickets append fields:

```go
type Service struct {
    Store  *store.Store
    Logger *slog.Logger
    Cfg    config.Config

    // Agents (this ticket).
    touchOnce map[string]time.Time // debounce LastSeenAt rewrites; protected by touchMu
    touchMu   sync.Mutex

    // Fields added by later tickets — declare them here as zero values:
    //   Cache         *cache.ProjectCache  // T04
    //   Embed         embed.Provider       // T08 (interface) — populated in T10's wiring
    //   Worker        *worker.Worker       // T10
    //   LearningsIdx  *vecindex.Index      // T11 / T10 wires it
    //   SummaryIdx    *vecindex.Index      // T11
}

func New(cfg config.Config) (*Service, error) {
    // Build Store, init logger, return Service with foundational fields populated.
    // Later-ticket fields stay nil until those tickets ship and add their construction.
}
```

**Convention for later tickets**: when T04/T08/T10/T11 land, they edit `service.go` to add their field to the struct AND extend `New` to construct it. The struct definition is shared but additive — no ticket *replaces* it.

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
