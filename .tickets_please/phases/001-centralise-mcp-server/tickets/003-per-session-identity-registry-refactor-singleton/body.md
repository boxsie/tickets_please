## Goal

Refactor `internal/mcptools/identity.go` from a process-level singleton to a registry keyed by MCP session ID. Each entry holds: agent UUID, agent metadata (model, client, etc.), and a project default. Tool handlers extract the per-session identity from request context, not from a shared struct on `Tools`.

## Why

The current `Identity` struct caches one session per process. With HTTP/SSE transport and multiple concurrent clients (next ticket), each connection needs its own session — they can't share the singleton. This is the load-bearing refactor that unblocks the HTTP transport.

## Scope

- `internal/mcptools/identity.go`:
  - Replace `Identity` struct with `Registry` holding `map[string]*Session` under a `sync.RWMutex`.
  - `Session` carries: `AgentID`, `AgentKey`, `AgentName`, `Metadata` (map), `ProjectSlug` (default), `ExpiresAt`.
  - `Registry.Get(sessionID)` / `Register(sessionID, ...)` / `Touch(sessionID)` / `Remove(sessionID)` API.
- `internal/mcptools/tools.go`:
  - `callWithRetry` extracts session ID from `*http.Request` context using `mcpserver.ClientSessionFromContext` (mark3labs/mcp-go provides this).
  - On unknown/expired session, error with `ErrUnauthenticated` so the new `register_agent` tool (separate ticket) can be the recovery path. (No more silent re-registration — clients must explicitly register.)
- `cmd/tickets_please/main.go` (stdio path):
  - Synthesise a fixed session ID for stdio (e.g. `"stdio"`) so the same code path applies.
  - On stdio startup, call `Registry.Register("stdio", ...)` once with values from env (`MCP_AGENT_NAME`, `MCP_AGENT_KEY`). Backwards-compat for stdio clients that don't know about `register_agent`.

## Notes

- Don't remove the env-var-driven registration — it's the stdio fallback and CI/test path. HTTP path won't use it.
- mcp-go's `mcpserver.ClientSessionFromContext` returns nil for stdio (no per-connection session) — that's why we synthesise a fixed ID.
- This ticket does NOT add the `register_agent` MCP tool yet — that's the next ticket. After this ticket, HTTP clients can't register; only stdio works.

## Verification

- Stdio mode unchanged behaviourally: `tickets_please mcp` from a repo, `who_am_i` returns the env-driven agent.
- Unit tests for `Registry`: register, get, expire, evict.
- Race-detector run (`go test -race ./internal/mcptools/...`) clean under concurrent register/get.

## Out of scope

- The `register_agent` MCP tool itself (separate ticket).
- Project default resolution from `register_agent` (separate ticket).
- HTTP transport (separate ticket).
