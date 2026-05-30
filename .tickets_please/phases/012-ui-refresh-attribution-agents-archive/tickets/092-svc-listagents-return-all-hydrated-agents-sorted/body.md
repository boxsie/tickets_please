The store has `WalkAgents` but `svc` only exposes `GetAgent(id)`. Add the list method that backs the agents page.

## Acceptance

- `Service.ListAgents(ctx) ([]*domain.Agent, error)` walks the agent store, hydrates each, returns sorted by `LastSeenAt` desc (nulls last by `CreatedAt`).
- Also returns per-agent computed counts (cheap aggregates the UI needs): `TicketsCreated`, `TicketsCompleted`, `CommentsAuthored` — computed by walking each project's tickets/comments and tallying by `CreatedBy.ID` / `CompletedBy.ID` / `Author.ID`. Use the cache layer.
- A second method `Service.AgentActivity(ctx, agentID, limit) ([]ActivityItem, error)` returns a unified timeline (typed union: TicketCreated, TicketCompleted, CommentAdded, AgentRegistered) ordered newest-first.
- Tests cover: empty store, multiple agents, count accuracy, activity ordering.
- Backed by an existing-or-new cache so the agents page doesn't full-walk on every request — invalidate on agent registration, ticket completion, comment.

## Hints

- The cache layer in `internal/cache/` already keys per-project; agent activity is cross-project so it needs its own keyed view.
- For Phase 1 W3 realtime, this svc method is the source the SSE `AgentSeen` patches consult.
