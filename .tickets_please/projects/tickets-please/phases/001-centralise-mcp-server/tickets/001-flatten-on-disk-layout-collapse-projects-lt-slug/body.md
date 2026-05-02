## Goal

Drop the redundant `projects/<slug>/` folder under each repo's `.tickets_please/`. After this ticket, a project's data lives directly at `.tickets_please/{project.yaml,summary.md,phases/,tickets/,...}` — one project per data root.

## Why

With the centralisation pivot, each repo's `.tickets_please/` only ever holds one project (the repo IS the project). The slug folder under a `projects/` parent is one level of nesting too many. Collapsing it makes the disk layout match the mental model and is foundational — every later ticket assumes the new shape.

## Scope

- `internal/store/projects.go`: rewrite `WalkProjects`, `ProjectDir`, and any path helpers to root at the data dir directly (no `projects/` subdir, no `<slug>/` subdir). A `Store` is now rooted at one project's `.tickets_please/`.
- `internal/store/`: any other path constants that reference `projects/<slug>/` — search for `"projects"` string literals and review.
- `internal/cache/projectcache.go`: project resolution no longer walks a `projects/` directory; it uses an in-memory registry keyed by slug (registry-population is ticket "Multi-root project registry"; here we just remove the disk walk).
- `cmd/tickets_please/main.go`: `runInit` no longer creates a `projects/` subdir.
- `internal/store/records.go`: ticket/phase paths drop the `projects/<slug>/` prefix.
- Update SPEC.md "Data layout" section + `.tickets_please/README.md` template in `main.go`.

## Migration tool

Add a `tickets_please migrate <repo-path>` subcommand to `cmd/tickets_please/main.go`. For each repo, it:
1. Detects `<repo>/.tickets_please/projects/<slug>/`. If exactly one slug folder, hoist its contents to `<repo>/.tickets_please/`. If multiple, error (one repo, one project).
2. Removes the now-empty `projects/` dir.
3. Idempotent: detects already-flat layout and no-ops.
4. Has a `--dry-run` flag.

Agents migration (moving `<repo>/.tickets_please/agents/` to the central path) is the next ticket's job — this ticket only touches the project layer.

## Verification

- Build, run `tickets_please mcp` in a repo that's been migrated; `list_phases tickets-please` works without errors.
- Run `migrate <repo>` twice — second is a no-op.
- `git diff` after migration shows files moved (git rename detection should pick up most as renames).
- Stdio mode still works end-to-end (we don't add HTTP yet).

## Notes

- The stdio session model stays the same in this ticket. Per-session refactor is a separate ticket.
- Auto-commit-per-mutation behaviour is preserved — it just operates on the new flat paths.
