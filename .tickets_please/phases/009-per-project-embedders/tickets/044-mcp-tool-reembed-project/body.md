## Goal

Expose `Service.ReembedProject` over MCP so agents can trigger a wipe-and-rebuild without going through the web UI.

## Scope

- `internal/mcptools/tools.go` — register after `delete_project`:
  ```go
  s.AddTool(mcp.NewTool("reembed_project",
      mcp.WithDescription("Delete all *.embedding.json sidecars in a project and enqueue async re-embed using the project's currently configured embedder. Use after switching embed_provider/embed_model in project.yaml, or to recover from corrupted sidecars."),
      mcp.WithString("project_id_or_slug", mcp.Description("Project id or slug; optional if a session is bound")),
  ), t.handleReembedProject)
  ```
- Handler mirrors `handleDeleteProject`: `resolveProjectSlug` → `callWithRetry(svc.ReembedProject)` → `jsonResult({"reembed_project": slug, "status": "re-embedding enqueued"})`.
- Tool count bookkeeping (per prior learnings, three places in lockstep):
  - `cmd/tickets_please/main.go` `totalTools`
  - `internal/mcptools/tools.go` doc comment ("registers all N tools")
  - `internal/mcptools/tools_test.go` `expectedTools`
  Net effect after W4-T1 dropped `search_projects` and W3-T2 adds `reembed_project`: 28 tools (verify the W4-T1 ticket also bumps these in lockstep — coordinate with whoever picks them up).

## Tests

- `internal/mcptools/tools_test.go`: canonical-list test will catch the rename.
- New `mcptools_reembed_project_test.go`: round-trip via the MCP test harness; assert the response shape.

## Done when

- `make build` + `go test ./...` green.
- Calling `mcp__tickets_please__reembed_project` against this project's mount returns the expected JSON and the worker rebuilds all sidecars.
