## Testing evidence
Ran npm --prefix frontend run typecheck successfully, npm --prefix frontend run test -- --run successfully, npm --prefix frontend run build successfully, and go test ./... successfully. Ticket cards now open a drawer, and the drawer shows body, column, phase, wave, dependencies, blockers, attribution, timestamps, comments, and completion fields only when present. Drawer state is reflected in the URL ticket query parameter.

## Work summary
Added a right-side ticket detail drawer with full metadata, body, dependency/blocker/parallel sections, attribution, completion field rendering, URL ticket state, edit entry point, and a comments section hook. Cards now open the drawer separately from drag and edit interactions.

## Learnings
The detail drawer can use the already-loaded board ticket shape for the first pass; that avoids an extra get_ticket request and keeps drawer open/close state tied to the current board scope. URL ticket state is enough for simple restore while the selected wave contains that ticket.
