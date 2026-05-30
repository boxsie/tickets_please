Wire the service layer to publish events into the hub so the frontend has something to react to.

## Acceptance

- `internal/svc` gains an `eventbus.Publisher` dep (interface — hub implements it).
- Publish points (each emits a typed event with payload + topic):
  - `MoveTicket` → `TicketMoved{ticket_id, from, to, by_agent, by_user}` on `ticket:{id}`, `phase:{phase_id}` (if any), `project:{project_id}`.
  - `CompleteTicket` → `TicketCompleted{...}` on same topics.
  - `CreateComment` → `CommentAdded{ticket_id, comment_id, kind, author}` on `ticket:{id}`.
  - `ArchiveTicket` / `UnarchiveTicket` → `TicketArchived{...}` / `TicketUnarchived{...}`.
  - `RegisterAgent` → `AgentRegistered{agent_id, name, user_id}` on `global:agents`.
  - Agent `LastSeenAt` bumps → `AgentSeen{agent_id, last_seen_at}` on `global:agents` (debounced to once per 10s).
- Tests verify each mutation path publishes the right event (use a recording fake publisher).
- Events are fire-and-forget: publish failure logged at WARN but never blocks the mutation.

## Hints

- Don't put HTTP / template-render concerns in the event payload — payloads are pure data the frontend (or other subscribers) project into UI patches.
- Hub `Publish` should be non-blocking even if a subscriber buffer is full (drop or async).
