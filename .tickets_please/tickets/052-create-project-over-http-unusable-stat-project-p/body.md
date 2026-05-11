## Symptom

A fresh HTTP MCP session calls `create_project` from a machine other than the server host. Server returns:

```
invalid argument: repo_path /some/path: stat /some/path: no such file or directory
```

There is no in-band way to recover — the documented "bootstrap escape valve" (`create_project` needs no session) is unreachable for any client that isn't local to the server's filesystem. Operators are forced into out-of-band `exec ... mkdir` workarounds.

`register_agent` exhibits the same failure for the same reason, but it has a defensible bootstrap story ("call create_project first"). `create_project` does not — it IS the bootstrap.

## Reproduction

1. Run `tickets_please serve --addr :8765` on a remote host.
2. From any other machine, point an MCP HTTP client at the server.
3. Call `create_project` with `project_path=/some/absolute/path/that/doesnt/exist/on/the/server`.
4. Server rejects with `stat ... no such file or directory`.

## Root cause

`internal/mcptools/tools.go` (the `create_project` handler and the equivalent block in `register_agent`) does an unconditional `os.Stat(project_path)` and errors if the path is missing or not a directory:

```go
if info, statErr := os.Stat(projectPath); statErr != nil || !info.IsDir() {
    return mcp.NewToolResultError(fmt.Sprintf(
        "invalid argument: project_path %q does not exist or is not a directory",
        projectPath)), nil
}
```

Sensible for the original stdio model — client and server share a filesystem. Not sensible for the HTTP/multi-client model where `project_path` is effectively a string identifier that may name a directory on the client's machine.

The tool docstring even hints at the asymmetry: `<project_path>/.tickets_please/ will be created if it doesn't exist.` — but the stat check rejects the call before that creation can run.

## Impact

- Deployed HTTP server (one process, many clients — explicit project goal) cannot accept a fresh project from a remote agent without server-side filesystem access.
- Documented "HTTP clients should call register_agent once on connection" workflow is broken for any project that doesn't already exist server-side.
- Pushes operators back to stdio or to manual `mkdir` workarounds, defeating the central-server win HTTP mode was designed to deliver.

## Proposed fix (Option 1 — make create_project actually create the dir)

For `create_project` specifically: if the directory at `project_path` doesn't exist, the server creates it (and any parents) under a configured root, then writes `.tickets_please/project.yaml` as today. The stat check becomes "if it exists, ensure it's a directory" rather than a hard precondition.

- Stdio behaviour preserved: an existing local repo path is used as-is.
- For HTTP clients passing a non-existent path, the server materialises it on its persistent volume. That path becomes the project's stable identifier — any future `register_agent` with the same string binds to the same project.
- Configurable root prevents the server from being a footgun: e.g. `--remote-project-root /data/projects` (default `${DATA_ROOT}/projects/`). Reject `create_project` calls whose `project_path` is non-existent AND outside the configured root.

## Acceptance

- `create_project` over HTTP with `project_path=/whatever/never/existed` succeeds. Server creates the dir (under the configured root) and writes `.tickets_please/project.yaml` inside.
- `register_agent` with the same `project_path` string binds the session to the newly-created project.
- Stdio path semantics unchanged: passing an existing local-repo path uses that exact path, no rewriting.
- Out-of-root remote paths rejected with a clear error pointing at `--remote-project-root`.
- New unit test covering: (a) stdio + existing path, (b) stdio + missing path → reject, (c) HTTP + missing path → create under root, (d) HTTP + missing path outside root → reject.

## Files likely affected

- `internal/mcptools/tools.go` — `create_project` and `register_agent` handlers (the stat check and its counterpart).
- `internal/cfg/config.go` (or wherever `DATA_ROOT` is wired) — add `RemoteProjectRoot` field.
- `cmd/tickets_please/main.go` — `--remote-project-root` flag.
- `internal/mcptools/instructions.go` — clarify bootstrap docs once the fix lands.

## Out of scope

- Multi-tenancy / per-agent ACLs on which projects an agent can create. Today any caller can create any project; preserved.
- The mount registry (`internal/svc/registry.go`) — already handles persistent project paths fine; bug is purely on the create path.
