## Testing evidence
Ran npm --prefix frontend run typecheck successfully, npm --prefix frontend run test -- --run successfully, npm --prefix frontend run build successfully, and go test ./... successfully. The UI now groups the selected ticket set into todo, in_progress, testing, and done columns with counts, empty states, ready/blocked badges, wave metadata, and a refetch control.

## Work summary
Replaced the Wave 2 ticket preview list with responsive board columns. Added per-column headers, stable empty states, ticket cards with title/body/wave/status metadata, ready and blocked indicators, done badges, and a manual refresh action wired to the ticket query.

## Learnings
The board can stay a pure rendering layer for this ticket by grouping the existing list_tickets result client-side. That keeps the later drag ticket focused on interactions and mutation/refetch behavior rather than data loading.
