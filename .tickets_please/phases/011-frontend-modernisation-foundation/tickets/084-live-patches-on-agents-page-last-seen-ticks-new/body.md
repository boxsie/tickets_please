The /agents page is built in Phase 2 W3, but the realtime hooks for it live here so Phase 1 W3 lands a complete realtime story.

## Acceptance

- `/agents` page subscribes to `global:agents`.
- On `AgentRegistered`: new row inserted at the top; brief highlight animation.
- On `AgentSeen` (the debounced last-seen-at bump): the row's "last seen" cell updates with the new relative time.
- `/agents/{id}` page subscribes to `agent:{id}` (and `global:agents` for the seen tick): when this agent creates or completes a ticket / posts a comment, the activity feed prepends it.
- This ticket is implementation-only — the pages themselves are built in Phase 2 W3 ([[agents-index-page]] / [[agents-detail-activity-feed]]). Coordinate so the page templates emit the right selectors.

## Hints

- `AgentSeen` patches are small — just one `<time>` element re-render per event.
- Don't pre-build the page here — just land the SSE subscribe + patch helpers and document the contract.
