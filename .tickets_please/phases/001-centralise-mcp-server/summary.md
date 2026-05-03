## Phase: Centralise MCP server

Replace the per-cwd stdio subprocess model with a single long-running HTTP MCP server that all clients (Claude Code, Codex, etc.) connect to. Project data stays in each repo's `.tickets_please/`; only the agent registry centralises.

## Pain points this fixes

1. **Codex sandbox blocks Ollama.** Embedding calls happen inside the MCP process; sandboxed subprocesses can't reach `localhost:11434`. A central server outside any sandbox does.
2. **Memory duplication.** N MCP subprocesses each holding 16-project LRU + resident vector indexes. One shared cache instead.
3. **Shallow agent metadata.** Today only `MCP_AGENT_NAME=Claude Code` is captured. We want model/version/client_name self-reported by the LLM at register time.

## Target architecture

```
Per-repo:                               Central (~/.tickets_please/):
  .tickets_please/                        config.yaml
  ├── project.yaml      ← marker          agents/<uuid>.yaml
  ├── summary.md
  ├── phases/...
  └── tickets/...

  Claude Code ─┐
  Codex     ──┼─► HTTP ─► tickets_please serve ─► Ollama
  Other LLM ───┘             │
                             ├─► /repo-A/.tickets_please/
                             └─► /repo-B/.tickets_please/
```

## Settled design decisions

- **Project context per session**: client calls `register_agent({model, model_version, client_name, project_path})` on connect; server reads `<project_path>/.tickets_please/project.yaml` to get the slug; tools default to that project; explicit `project_id_or_slug` still wins.
- **Disk shape**: collapse `.tickets_please/projects/<slug>/*` → `.tickets_please/*` per repo (the redundant slug folder dissolves under one-project-per-repo). Agents move out to central path.
- **Discovery**: no separate registry file; `project.yaml` at root of each repo's `.tickets_please/` IS the marker. Server learns paths from `register_agent` calls and rebuilds in-memory map on restart.
- **Library reuse**: `mark3labs/mcp-go` v0.50.0 ships `StreamableHTTPServer` + per-connection sessions; reuse rather than rebuild.
- **Stdio preserved** as a backwards-compat path with a synthesised single session ID.

## Out of scope for this phase

- Auth (deferred — localhost-only v1; bearer-token middleware can bolt on via mcp-go middleware later).
- Process supervision / systemd unit (manual start for v1).
- Home-server deployment (architecture supports it; build it after localhost works).

## Reference

- Full plan: `/home/dan/.claude/plans/so-break-it-down-enchanted-pixel.md`
- Critical files: `cmd/tickets_please/main.go`, `internal/mcptools/{identity,tools}.go`, `internal/svc/service.go`, `internal/store/{projects,agents}.go`, `internal/cache/projectcache.go`, `internal/config/config.go`.
