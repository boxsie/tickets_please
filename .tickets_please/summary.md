## tickets_please — meta-project

This project tracks evolution of the `tickets_please` system itself (dogfood). The system is a single-binary Go MCP server that backs a Trello-shaped, LLM-first ticketing model with semantically-searchable completion learnings.

## Architecture today

- **Single Go binary** at `cmd/tickets_please/main.go`, subcommands `mcp` (stdio MCP server) / `init` / `check`.
- **MCP library**: `github.com/mark3labs/mcp-go` v0.50.0. Currently stdio-only; the library also ships `StreamableHTTPServer` and `SSEServer` for HTTP transport.
- **On-disk data** lives under `.tickets_please/` in the cwd of the launching client. Layout: `projects/<slug>/{project.yaml,summary.md,phases/,tickets/,...}`, `agents/<uuid>.yaml`, `.staging/`.
- **Per-process Identity singleton** in `internal/mcptools/identity.go` — one MCP agent session per binary instance, registered at startup from env (`MCP_AGENT_NAME`, `MCP_AGENT_KEY`).
- **28 MCP tools** across projects, phases, tickets, comments, search, introspection. 13 of them require `project_id_or_slug` on every call (no "current project" concept server-side).
- **Embedding** via Ollama (default `nomic-embed-text`) or OpenAI; the HTTP call happens inside the MCP process.
- **Auto-commit per mutation** when `data_dir` is inside a git repo — every move, create, complete lands as a commit attributed to the calling agent.

## Active pain points motivating current work

1. **Codex sandbox can't reach Ollama at localhost:11434.** Each MCP client spawns its own stdio subprocess; the embedding call happens inside that sandboxed subprocess and fails. Centralising the server outside any client's sandbox fixes this — only the central process needs Ollama access.
2. **Wasteful duplication.** Every spawned subprocess holds resident vector indexes and an LRU cache of up to 16 projects. N agents × N subagents × big indexes is silly.
3. **Shallow agent metadata.** `MCP_AGENT_NAME=Claude Code` is set once at process startup and that's all attribution captures. The LLM itself knows its model name (Opus 4.7, Sonnet 4.6, etc.); the env var doesn't. Tickets get authored with no model/version trail.

## Direction

Move to a centralised long-running HTTP MCP server (one process, many clients), with project data still living per-repo (`.tickets_please/project.yaml` is the marker the server reads to bind a session to a project). Agents store moves to a central `~/.tickets_please/agents/`. Per-session Identity replaces the process-singleton. New `register_agent` MCP tool lets clients self-report rich metadata (model, version, client name, project path). Stdio mode preserved for backwards compatibility.

Full plan: `/home/dan/.claude/plans/so-break-it-down-enchanted-pixel.md`.

## Constraints

- **Hobby project**, no customers. Lean toward fun tech and clean cuts over backwards-compat noise.
- Single Go binary, no daemons/databases. Filesystem is the source of truth.
- `mark3labs/mcp-go` v0.50.0 is the MCP library — its primitives (`StreamableHTTPServer`, per-connection sessions) should be reused rather than rebuilt.
