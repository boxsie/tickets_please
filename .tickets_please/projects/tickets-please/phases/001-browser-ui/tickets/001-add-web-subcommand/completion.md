## Testing evidence
Ran go test ./... successfully. Ran go run ./cmd/tickets_please help and confirmed the web subcommand appears. Started go run ./cmd/tickets_please web --addr 127.0.0.1:8787 and verified curl http://127.0.0.1:8787/healthz returned {"ok":true}. Sent Ctrl-C and observed the web server stopped log.

## Work summary
Added a tickets_please web subcommand with --addr parsing, default 127.0.0.1:8787 bind, shared svc.Service construction, HTTP server startup, signal-aware graceful shutdown, and a small internal/web handler package with /healthz and a placeholder root page.

## Learnings
Keeping the HTTP handler in internal/web gives the API tickets a clean place to grow without crowding cmd/main.go. Localhost smoke tests need to run outside the sandbox here, but the server lifecycle itself stayed simple once net.Listen owned the address before Serve.
