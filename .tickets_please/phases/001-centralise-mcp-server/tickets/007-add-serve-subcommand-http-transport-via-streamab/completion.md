## Testing evidence
Subagent commit 1b28d27 merged into main. curl http://localhost:18765/healthz returned ok HTTP 200. Startup logs show addr data_root and tools count. SIGTERM produced clean shutdown log. go build go vet go test all clean. Race detector clean across mcptools store svc.

## Work summary
Added serve subcommand in cmd tickets_please main.go runServe. Flags addr default 8765 and data-root overrides cfg DataRoot. Builds Service via NewWithEmbed and Tools MCPServer same as runMCP, wraps with mcpserver NewStreamableHTTPServer, mounts at slash mcp on http ServeMux alongside healthz. Graceful shutdown via signal NotifyContext with 10s drain. Version bumped 0.2.0 to 0.3.0. SPEC got Centralised mode subsection with claude mcp add transport http snippet; README got Claude Code centralised HTTP wiring section.

## Learnings
mcp-go NewStreamableHTTPServer returns a value that itself implements http Handler — mount directly with mux.Handle slash mcp httpMCP, no wrapper needed. Library also offers httpMCP.Start addr for no-mux path; we used custom mux only to add healthz. http.Server.Shutdown is sufficient (closes connections); no need to also call streamable Shutdown. Per mcp-go: only GET handlers (the SSE listening leg) trigger session-registration hooks; POST tool calls do not — fine since agent identity arrives via register_agent tool calls not session hooks. ClientSessionFromContext already wired by mcp-go for HTTP-mode tool invocations from T002 work; no extra plumbing needed.
