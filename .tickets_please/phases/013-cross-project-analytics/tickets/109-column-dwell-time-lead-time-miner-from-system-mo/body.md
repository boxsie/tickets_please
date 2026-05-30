Every column move is captured as a `system_move` comment with `from_column`, `to_column`, `created_at`. Walking those for a ticket reconstructs the full lifecycle timeline. Aggregate that across all tickets to learn how long work spends in each column.

## Acceptance

- `svc.TicketLifecycle(ctx, ticketID) (*domain.Lifecycle, error)` returns:
  - `Transitions []Transition{From, To, At, ByAgent}`.
  - `DwellTimes map[Column]Duration` (total time spent in each column, summing across re-entries).
  - `LeadTime Duration` (created → completed-at).
- `svc.ProjectDwellHistograms(ctx, projectID, since time.Time) (map[Column][]Duration, error)` collects per-column dwell-times across every completed ticket in the window — used to plot distributions.
- Walk approach: `list_comments` filtered to `kind in (system_move, system_completion)` for the ticket, sort by created_at, fold over them to build transition list.
- Edge cases handled: ticket re-enters a column (dwell sums), ticket completion (`system_completion` is the terminal "to_column: done" event), tickets with no system_move comments (created → done directly via complete_ticket; dwell = (CompletedAt - CreatedAt) in `todo`).
- Tests with hand-crafted comment streams covering each edge case.

## Hints

- The cache layer should key this per-ticket; `ProjectDwellHistograms` reads from per-ticket caches in parallel.
- p50/p90 helpers from [[project-metrics-aggregator]] take a `[]Duration` so they can be reused here.
