## Goal

Add a 29th MCP tool: `register_agent`. Clients call it once on connection. It captures rich agent metadata and binds the session to a project by reading the repo's `project.yaml`.

## Why

Today's startup-once env-var registration captures only `MCP_AGENT_NAME=Claude Code`. The LLM itself knows its model name (Opus 4.7, Sonnet 4.6), version, and which client wraps it — that information is only available at runtime, not at server-start. A tool the LLM calls fixes that.

Also: with multi-client HTTP, each connection needs to declare which project it's working on. Reading `<project_path>/.tickets_please/project.yaml` is how the server learns the slug → repo path mapping for this session.

## Tool schema

```
register_agent(
  model              : string,        // e.g. "claude-opus-4-7"
  model_version      : string?,       // e.g. "20251201" (optional)
  client_name        : string,        // e.g. "Claude Code", "Codex"
  client_version     : string?,       // e.g. "1.0.0" (optional)
  project_path       : string,        // absolute path; server reads <path>/.tickets_please/project.yaml
  agent_key          : string?,       // optional; defaults to "<client>:<rand>"
  agent_name         : string?,       // optional display name; defaults to client_name
) -> { session_id, agent_id, project_slug, expires_at }
```

## Scope

- `internal/mcptools/tools.go`: register the new tool. Handler:
  1. Extract MCP session ID from request context (mcp-go's `ClientSessionFromContext`).
  2. Read `<project_path>/.tickets_please/project.yaml` → get slug. If missing, error: "no .tickets_please/project.yaml at <path>".
  3. Open a `Store` rooted at `<project_path>/.tickets_please/` (cache it in the multi-root registry — see "Multi-root project registry" ticket; for now, just open and discard if needed).
  4. Build agent metadata map: `{model, model_version, client_name, client_version, hostname, project_path}`.
  5. Call `service.RegisterAgent(...)` to create the agent session in the central agents store.
  6. Insert into the per-session `Registry` from the previous ticket: `{AgentID, AgentKey, AgentName, Metadata, ProjectSlug, ExpiresAt}`.
  7. Return JSON.
- `internal/mcptools/identity.go`: extend `Session` with the project-path field if needed for re-resolution on store eviction.
- `internal/mcptools/tools.go`: extend `who_am_i` to surface model, model_version, client_name, client_version, project_slug, project_path.
- Update `internal/mcptools/instructions.go` (or wherever `ServerInstructions` lives): document `register_agent` as the first call any HTTP client should make. Stdio clients can skip if env vars are set.

## Stdio backwards-compat

Stdio mode synthesises session ID `"stdio"` and pre-registers from env vars. If a stdio client also calls `register_agent`, the call updates the existing session in place (idempotent, replaces metadata).

## Verification

- HTTP server (next ticket once built): client connects, calls `register_agent({model: "claude-opus-4-7", client_name: "Claude Code", project_path: "/abs/path/to/repo"})`, gets `session_id` + `project_slug`. Subsequent `who_am_i` reflects the model.
- Calling without `project_path` errors clearly.
- Calling with a path that has no `.tickets_please/project.yaml` errors clearly.
- Calling twice replaces metadata in place (last write wins) without creating a second session.

## Notes

- `agent_key` uniqueness still enforced by `AgentStore` — collisions get the random suffix added.
- Validation: model and client_name required, project_path must exist and contain the marker file.
- Don't validate that `model` matches a known list — let it be free-form. Future learnings/audit can use whatever the LLM declares.
