## Goal

Decouple the agents-on-disk registry from per-project storage. After this ticket: agents live in a central path (default `~/.tickets_please/agents/`) and are shared across all projects the server serves. Project stores no longer touch agents.

## Why

Centralised server serves many projects from many repos. An agent session is global to the server, not scoped to a project — a single Claude Code session might touch tickets in multiple projects. Today `agents/<uuid>.yaml` lives inside the per-repo `.tickets_please/`, which doesn't make sense once the server serves multiple repos.

## Scope

- `internal/store/agents.go`: extract into a standalone `AgentStore` rooted at a configurable path (not the project data dir). Same on-disk schema as today (`<root>/agents/<uuid>.yaml`).
- `internal/store/store.go` (or wherever `Store` is composed): `Store` no longer holds the agent store. Service composes both: `Store` for project data, `AgentStore` for agent sessions.
- `internal/svc/service.go`: `Service` constructor takes both. Wire all agent-related calls (`RegisterAgent`, `LookupAgent`, etc.) through `AgentStore`.
- `internal/config/config.go`: add `data_root` config key (default `~/.tickets_please`). This is the central root holding `agents/` and `config.yaml`. Distinct from any per-repo project location.
- `cmd/tickets_please/main.go`: `runInit` creates `<data_root>/agents/` rather than per-repo `<repo>/.tickets_please/agents/`.

## Migration

Extend the migrate subcommand from the previous ticket: also move `<repo>/.tickets_please/agents/*.yaml` to `<data_root>/agents/`. Skip files that already exist there (idempotent). Remove the now-empty per-repo `agents/` dir.

## Verification

- Stdio MCP still registers an agent on startup; `who_am_i` returns valid identity. Agent yaml lands in the central path, not the repo's `.tickets_please/`.
- Migrate a repo with old agent yamls; verify they end up in `~/.tickets_please/agents/` and the repo's `.tickets_please/agents/` is removed.
- Re-running migrate doesn't duplicate or error.
- Concurrency smoke: two stdio MCPs from two different repo cwds register simultaneously; both land in the central agents dir; uniqueness check holds.

## Notes

- The lock at the data-root level (already present per `internal/store/lock.go`) covers agent writes. Per-project flocks remain unchanged.
- This ticket assumes the flat per-repo layout from the previous ticket is in place.
