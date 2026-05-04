## Goal

Add a "Bootstrapping a new project" section to `ServerInstructions` so the cold-start flow is in every LLM's context every turn, not only when an error fires.

## File

`internal/mcptools/instructions.go` — the `ServerInstructions` const.

## Proposed addition

Insert a new section after "Optional structure" and before "Identity". Rough draft:

```markdown
## Bootstrapping a new project

Cold-starting in a repo with no `.tickets_please/project.yaml` requires a specific dance because `create_project` needs a session and `register_agent` needs `project.yaml`:

1. Launch `tickets_please mcp` from a stdio client (Claude Code, etc.) — the binary pre-registers a session at startup, no `project.yaml` required.
2. Call `create_project` from that stdio session. This writes `project.yaml` and mounts the project.
3. From then on, any client (including HTTP) can call `register_agent` with the repo's `project_path` and the existing tools work normally.

If you hit `no .tickets_please/project.yaml at <path>` from `register_agent`, that's the trigger to do the above — not a sign the system is in a weird state.
```

Keep it tight. The instructions are persistent context — every word costs tokens.

## Why

`ServerInstructions` is what `mcp-go` injects into the model's context every turn. Today it documents steady-state; cold-start is the gap. Filling it means agents know the bootstrap flow before they even hit the error.

## Verification

- `go build ./...` (string compiles).
- `go test ./internal/mcptools/...` green — there may be a test that snapshots / length-checks `ServerInstructions`; update if so.
- Manual smoke: from a fresh stdio session in a tempdir, the agent should see the new section in its `initialize` response.

## Out of scope

- Server error messages (sibling ticket — they reinforce this guidance from the error path).
- The `dataDirReadme` (sibling ticket — README-level docs).
