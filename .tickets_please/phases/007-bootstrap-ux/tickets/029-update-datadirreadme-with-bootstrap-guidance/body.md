## Goal

Add a "How to bootstrap from here" section to the per-repo README written by `tickets_please init`, so a human (or agent) opening the data dir sees the cold-start flow without leaving the file.

## File

`cmd/tickets_please/main.go:560` — the `dataDirReadme` const.

## Proposed addition

Append a new section after the existing layout description. Rough shape:

```markdown
## Cold-starting a fresh repo

If you've just run `tickets_please init` in a repo with no project yet, the data
dir has only `.staging/` and this README. To create the actual project record:

1. Launch the MCP server in stdio mode from this repo: `tickets_please mcp`.
2. From your LLM client, call `create_project` with `slug`, `name`, and a
   substantive `summary` (≥200 chars — the summary is load-bearing context for
   future work).
3. After that, `project.yaml` exists and any MCP client (HTTP or stdio) can
   `register_agent` against this repo's path and use the full tool set.

The chicken-and-egg detail: `register_agent` reads `project.yaml`, so it can't
be called against a fresh repo. Stdio's session is pre-registered at process
start, which is the escape valve for cold-start.
```

Tone-match the existing README — it's terse, factual, no marketing voice. Keep formatting consistent (the existing const concatenates strings with `\n`; preserve that style or convert wholesale to a backtick raw string if cleaner).

## Why

Lowest leverage of the three changes — agents observed in the wild read live error messages over READMEs, and the `init` README only ships if someone runs `tickets_please init`. But it's the file a human opens when they're poking around the data dir, and it's nearly free to update.

## Verification

- `go build ./...` (the const compiles).
- `go test ./...` green (no test snapshots this README that I'm aware of, but check).
- Smoke: `tickets_please init /tmp/foo`, open `/tmp/foo/.tickets_please/README.md`, eyeball the new section reads cleanly.

## Out of scope

- Server error messages (sibling ticket).
- `ServerInstructions` (sibling ticket).
- The top-level repo `README.md` — that's a separate doc with broader scope.
