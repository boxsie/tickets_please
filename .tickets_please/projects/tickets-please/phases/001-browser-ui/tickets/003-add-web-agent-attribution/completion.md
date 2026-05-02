## Testing evidence
Ran go test ./... successfully. New web tests create a ticket and add a comment through the API, then assert created_by and author names are tickets_please_web. Read-only GET /api/projects is tested with no identity. Live web server startup registered the web agent before serving requests.

## Work summary
Added a web-specific Identity that registers tickets_please_web on startup, attaches its session to every mutating API handler, and retries once after ErrUnauthenticated by re-registering. Read handlers are session-free, and the attribution path is covered for ticket and comment mutations.

## Learnings
The web identity should stay distinct from the MCP identity so audit trails show whether a change came from browser use or MCP use. Retry-on-ErrUnauthenticated belongs at the web mutation boundary so individual handlers stay small.
