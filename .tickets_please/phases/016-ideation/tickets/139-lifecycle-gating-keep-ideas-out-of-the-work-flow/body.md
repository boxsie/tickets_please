An idea must not behave like work-in-progress. It never reaches `done` directly and never blocks/satisfies real work — it gets promoted first (next ticket).

## Changes
- **`internal/svc/tickets.go`** — `CompleteTicket` (~451): if the ticket's `Kind == KindIdea`, reject with a clear error pointing at `promote_idea` ("ideas can't be completed; promote it to a work ticket first"). This is the idea analogue of the move-to-done guard.
- **`internal/svc/tickets.go`** — `ListTickets` with `ready_only=true`: exclude ideas (they're never "ready work"). They're already hidden by the default kind filter, but make `ready_only` robust even if `include_ideas` is set.
- **`depends_on` semantics** — decide + document: an idea should not be addable as a `depends_on` target for a work ticket (or if allowed, it never counts as satisfied). Simplest: reject `depends_on`/`parallelizable_with` references to a `kind=idea` ticket at create/update time with a message to promote first. Keep it minimal.
- **`move_ticket`** — keep working for ideas (you can still shuffle/comment), but an idea stays effectively in `todo`; don't let it walk into `in_progress`/`testing` (optional — confirm during impl whether to hard-gate or leave loose).

## Notes
- Keep the guards in the service layer so both MCP and web inherit them.
- Don't over-build: the load-bearing guard is the `complete_ticket` rejection. The depends_on guard is the secondary correctness fence.

## Acceptance
- `complete_ticket` on an idea returns the promote-first error.
- `ready_only=true` never returns ideas even with `include_ideas=true`.
- Attempting to make a work ticket depend on an idea is rejected with a helpful message.
- Unit tests for each guard.
