## Testing evidence
Ran go test ./... successfully, including new internal/web handler tests. Started go run ./cmd/tickets_please web --addr 127.0.0.1:8787 and verified GET /api returned the documented endpoint list. Verified GET /api/projects/tickets-please/tickets?phase=browser-ui&wave=1 returned the expected real Wave 1 tickets. Tests also cover mutating success paths and invalid move comment error mapping.

## Work summary
Added the browser JSON API under /api with routes for projects, phases, waves, tickets, comments, move, complete, and search. Handlers call svc.Service directly, return stable snake_case JSON, parse query/body inputs, and map domain sentinel errors to typed HTTP JSON responses.

## Learnings
The standard library ServeMux path variables are enough for this first local API contract. Keeping formatting inside internal/web avoids exporting MCP-only helpers while still matching the same response shapes the frontend will expect.
