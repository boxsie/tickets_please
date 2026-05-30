Per-agent activity timeline. Click an agent on the index → see everything they did, across all projects, newest first.

## Acceptance

- New route `GET /agents/{id}` rendering an agent-detail page.
- Header: agent name, model, key, registered, last seen, acting-for user (if bound), aggregate counters.
- Activity feed (paginated, 50 per page) using `Service.AgentActivity`: each row is one TicketCreated / TicketCompleted / CommentAdded / AgentRegistered event with project context, timestamp, and a link.
- Each entry uses an icon for the activity type and the existing `ticket_card` / `comment` partials where it makes sense.
- A "Currently working on" callout at the top: any tickets in `in_progress` where `CreatedBy.ID == agent.id` OR where the agent has posted comments in the last 24h.
- Live updates via Phase 1 W3 ([[live-patches-agents-page]]).
- Tests cover the handler and pagination.

## Hints

- Activity items render in their native shape — keep visual consistency with the rest of the app.
- "Currently working on" is a heuristic; document it in a code comment.
