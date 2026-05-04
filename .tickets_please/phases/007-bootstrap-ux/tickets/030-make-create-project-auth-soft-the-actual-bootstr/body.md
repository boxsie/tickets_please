## Goal

The earlier docs-only fix (T1/T2/T3 in this phase) was the wrong scope. Pointing agents at "launch stdio mcp from a fresh repo" doesn't work when there's no `tickets_please` binary in PATH and the user is in a Claude Code session over HTTP — they hit the same wall, and the helpful error becomes a more polite dead-end.

The real fix: drop the auth requirement on `create_project` itself. It's the one mutation where there's nothing yet to authorize against — the project IS the attribution surface coming into existence. Every other mutation still requires a session, so attribution remains correct from the second mutation onward.

## What changed

- `internal/svc/middleware.go`: new `optionalSession` helper — returns `(ctx, nil, nil)` when no session is on the context, instead of erroring.
- `internal/svc/projects.go`: `CreateProject` swapped from `requireSession` to `optionalSession`. Nil-agent paths guarded: `CreatedByAgentID` left nil, `domain.Project.CreatedBy` left nil, auto-commit skipped (already nil-safe in `StageOp.Commit`).
- `internal/mcptools/tools.go`: `handleCreateProject` no longer routes through `callWithRetry`. If a session is registered for this MCP session it's threaded through for attribution; otherwise the call proceeds and svc handles the no-session case.
- Error messages and `ServerInstructions` rewritten — point at "call create_project (no session required)" instead of the stdio dance.
- `dataDirReadme` cold-start section rewritten the same way (two steps: create_project then register_agent).
- Tests: `TestCreateProject_RequiresSession` -> `TestCreateProject_NoSessionSucceedsWithNilCreatedBy`. `TestIntegration_AgentSessionExpiry` rewired to use `CreateTicket` (still-auth-required) since `CreateProject` no longer is. New `TestCreateProject_NoSessionSucceeds` at the MCP layer. Phrase-pins updated.

## Why

User was in `/home/dan/Documents/job_hunt` over an HTTP MCP session, called `register_agent` -> "no project.yaml" -> called `create_project` -> "no session registered" -> loop. The previous fix made the errors describe the loop accurately; this fix makes the loop go away.

## Verification

- `go test ./...` green across all packages.
- `go build ./...` clean.
- After deploy: from any MCP client in a fresh `.tickets_please/`-less repo, `create_project` returns a project record with `created_by` null, then `register_agent` succeeds, then all other tools work.

## Out of scope

- Auto-registering an agent inside `create_project` (would let one call do everything, but couples two concerns).
- Making `register_agent` itself auth-soft on missing project.yaml (no longer needed — the user can always create the project first).
- Removing the `register_agent` call from the bootstrap flow entirely (it still binds the session to a slug and captures model/client metadata).</body>
