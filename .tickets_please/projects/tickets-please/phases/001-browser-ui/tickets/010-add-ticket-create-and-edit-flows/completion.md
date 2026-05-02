## Testing evidence
Ran npm --prefix frontend run typecheck successfully, npm --prefix frontend run test -- --run successfully, npm --prefix frontend run build successfully, and go test ./... successfully. Frontend tests cover API-backed rendering and server validation display for create ticket. The create/edit dialog supports title, body, phase, wave, create-time depends_on, and parallelizable_with fields.

## Work summary
Added Radix Dialog create/edit ticket flows, API client mutations for create/update/phase assignment, a browser API endpoint for AssignTicketToPhase, edit buttons on cards, dependency and parallel ticket selectors for create, wave/phase editing, server error display, and query invalidation after saves.

## Learnings
Phase changes must call the dedicated AssignTicketToPhase service path because they produce audit comments and move ticket directories. Dependency editing is not currently available in svc.UpdateTicket, so the UI supports dependency selection during create where the service already accepts those fields.
