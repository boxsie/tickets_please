## Testing evidence
Ran npm --prefix frontend run typecheck successfully, npm --prefix frontend run test -- --run successfully, npm --prefix frontend run build successfully, and go test ./... successfully. The board uses dnd-kit DndContext, droppable columns, draggable non-done cards, a required move-comment dialog, and query invalidation after successful moves.

## Work summary
Added dnd-kit drag handling to board cards and columns, disabled dragging for done tickets, opened a comment dialog for non-done target columns, rejected empty comments before calling the move API, called POST /api/tickets/{id}/move with target_column and comment, and refreshed tickets/waves after moves.

## Learnings
Keep the drop handler side-effect free until a modal is confirmed. The active drag result should only choose the pending operation; the actual service mutation belongs in the comment modal submit path so empty comments never hit the move endpoint.
