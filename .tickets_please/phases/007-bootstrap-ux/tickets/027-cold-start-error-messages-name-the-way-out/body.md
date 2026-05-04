## Goal

Make the two errors that fire during cold-start explicitly describe the bootstrap flow so an agent can resolve the situation from the error text alone, without spelunking source code.

## Files

- `internal/mcptools/tools.go:314` — `callWithRetry` "no agent registered for session" error.
- `internal/mcptools/tools.go:1242` — `handleRegisterAgent` "no .tickets_please/project.yaml" error.
- `internal/mcptools/register_agent_test.go:141` — existing assertion on the "no project.yaml" message text; update.

## Proposed text

### `callWithRetry` (was: `unauthenticated: no agent registered for session %q; call register_agent first`)

Something like:

```
unauthenticated: no agent registered for session %q.
- Populated repo (has .tickets_please/project.yaml): call register_agent with the absolute project_path.
- Fresh repo (no project.yaml yet): bootstrap from a stdio launch of `tickets_please mcp` (its session is pre-registered at startup), call create_project there, then HTTP clients can register_agent for follow-on calls.
```

### `handleRegisterAgent` (was: `no .tickets_please/project.yaml at %s`)

Something like:

```
no .tickets_please/project.yaml at %s — this repo has no project yet.
To create one: launch `tickets_please mcp` from a stdio client (its session is pre-registered) and call create_project. After project.yaml exists, register_agent works for any client.
```

Exact wording is up to the implementer; the load-bearing details are: (a) name `create_project` as the bootstrap, (b) name stdio as the way to get a registered session without project.yaml, (c) say "after that, register_agent works."

## Why

See phase summary. Net effect: the agent's primary information channel during cold-start (error responses) names the escape valve instead of contradicting itself.

## Verification

- Unit test in `register_agent_test.go` updated to assert the new text contains the load-bearing phrases (`create_project`, `stdio`, `pre-registered` or equivalent).
- A new unit test for `callWithRetry`'s no-session error in `tools_test.go` (or wherever fits — locate by greping for `callWithRetry` test usage) asserting same.
- `go test ./internal/mcptools/...` green.

## Out of scope

- Fixing the underlying chicken-and-egg (separate follow-up phase).
- The `ServerInstructions` block (sibling ticket).
- The `dataDirReadme` (sibling ticket).
