## Goal

Add `tickets_please serve [--addr :8765] [--data-root ~/.tickets_please]` subcommand that starts a long-running HTTP MCP server using `mark3labs/mcp-go`'s `StreamableHTTPServer`. Stdio mode preserved as-is.

## Why

The whole reason for centralisation. One process, many clients connecting via HTTP, sharing caches and outbound network access (Ollama).

## Scope

- `cmd/tickets_please/main.go`:
  - New subcommand `serve`. Flags: `--addr` (default `:8765`), `--data-root` (default `cfg.DataRoot`).
  - Build `Service` (with central agent store + empty project mounts map — they populate via `register_agent`).
  - Tools registered same way as stdio (same handlers, same `Tools` struct).
  - Wrap with `mcpserver.NewStreamableHTTPServer(server, ...)`.
  - Mount on `http.ServeMux` at path `/mcp` (or whatever mcp-go defaults to — check the library).
  - Graceful shutdown: signal.NotifyContext + `http.Server.Shutdown` with a timeout. Drain in-flight tools, close `Service`.
- Logging: each tool call logs session ID + agent key + tool name. Existing slog handler is fine.
- Health endpoint: GET `/healthz` returns `200 OK` if Service is up. Useful for systemd/manual smoke.

## Out of scope

- Auth (deferred — localhost-only v1; bearer-token middleware via mcp-go's HTTP middleware can bolt on later).
- TLS (deferred — terminate via reverse proxy when home-server happens).
- systemd unit (manual start for v1).

## Verification

- `tickets_please serve --addr :8765` starts; `curl localhost:8765/healthz` → 200.
- Update Claude Code's MCP config: `claude mcp add --transport http tickets_please http://localhost:8765/mcp`. Restart, MCP connects.
- From Claude Code: `register_agent({model, client_name, project_path: this_repo})` → succeeds. `who_am_i` → reflects model. `list_tickets` → returns tickets from this repo's project.
- Two simultaneous clients (Claude Code + Codex with HTTP MCP wired up to same server) — each binds to its own project_path, each sees its own project's tickets.
- Codex sandbox test: confirm Codex (sandboxed, can't reach 11434) can talk to the server at localhost:8765 and that `search_learnings` succeeds because Ollama calls happen server-side.
- Stdio still works: `tickets_please mcp` from a repo continues to work for legacy.

## Notes

- README + SPEC.md need updating: new "Centralised mode" section with serve subcommand wiring and updated MCP client config snippets.
- Auto-commit-per-mutation continues to work in centralised mode: each mount's `Store` knows its repo path, runs `git -C <repoPath>` for the commit. The only change is the central process is the one running the git command rather than a per-client subprocess.
- `~/.tickets_please/agents/` doesn't need to be in a git repo — it's the central registry, no auto-commit there.
