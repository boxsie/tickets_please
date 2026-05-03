## Goal

For the 13 MCP tools that currently require `project_id_or_slug`, make the parameter optional. When omitted, fall back to the session's default project (set by `register_agent`). When present, the explicit value still wins.

## Why

The whole point of `register_agent({project_path})` is so the LLM doesn't have to think about which project on every single call. If it forgets, the session-bound default kicks in. Less ceremony per call, fewer mistakes.

## Affected tools (13)

`create_ticket`, `list_tickets`, `update_ticket`, `move_ticket`, `complete_ticket`, `assign_ticket_to_phase`, `get_project_summary`, `update_project`, `delete_project`, `load_project`, `list_phases`, `create_phase`, `get_phase`, `update_phase`, `delete_phase`, `list_waves`, `add_comment`, `list_comments`, `search_tickets`, `search_comments`, `search_learnings` (already optional). Verify exact list from `internal/mcptools/tools.go`.

## Scope

- `internal/mcptools/tools.go`:
  - For each affected tool, drop `mcp.Required()` from the `project_id_or_slug` schema.
  - Add a helper `(t *Tools) resolveProject(ctx, req)` that:
    1. Reads `project_id_or_slug` from req.
    2. If non-empty, returns it.
    3. If empty, looks up the session's `ProjectSlug` from the per-session `Registry`.
    4. If neither, errors: "no project bound to this session — call register_agent or pass project_id_or_slug".
  - Replace each handler's `req.RequireString("project_id_or_slug")` with `t.resolveProject(ctx, req)`.
- Tool descriptions: update text to mention "optional if register_agent has bound a project to the session".

## Verification

- Stdio: env-driven session has no project default → tools requiring project still error helpfully if param omitted (or stdio also gets a default via env var — see notes).
- HTTP: register_agent with project_path, then call `list_tickets` with no project param → returns the bound project's tickets.
- HTTP: register_agent with project_path, then call `list_tickets` with explicit param for a different registered project → returns that project's tickets (explicit wins).
- HTTP: no register_agent yet, call `list_tickets` → errors with the helpful "call register_agent first" message.

## Notes / open questions

- Stdio default project: should we also support `MCP_PROJECT_SLUG` env var that pre-binds the stdio session to a project? Keep stdio fully working without a register_agent call. Probably yes; cheap to add.
- Cross-project tools (`search_projects`, `list_projects`, `search_learnings` without filter) stay project-agnostic — they always operated globally.
