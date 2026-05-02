## Testing evidence
Ran npm --prefix frontend run typecheck successfully, npm --prefix frontend run test -- --run successfully, npm --prefix frontend run build successfully, and go test ./... successfully. Dropping a non-done card on the done column opens a completion modal instead of move_ticket. The modal requires testing evidence, work summary, and learnings of at least 10 characters before calling complete_ticket; the detail drawer renders completion fields when present.

## Work summary
Added the done-drop completion branch to the drag handler, a Radix completion modal with testing evidence, work summary, and learnings fields, client-side minimum-length checks matching the service rule, complete_ticket API client wiring, and query invalidation after successful completion. Added a detail drawer surface that displays completion fields and learnings for done tickets.

## Learnings
Completion should be a separate pending operation from normal moves because done is not a valid move_ticket target. Keeping the card in place until complete_ticket succeeds naturally preserves the previous column on validation or service failure.
