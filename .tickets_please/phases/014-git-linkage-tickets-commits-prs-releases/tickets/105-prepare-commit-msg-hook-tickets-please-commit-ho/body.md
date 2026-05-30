Make ticket-ref attribution automatic. When an agent has a ticket in_progress, the hook auto-injects the `tickets-please/NNN` footer into their next commit message — no remembering required.

## Acceptance

- `tickets_please commit-hook install [--repo PATH]` installs `prepare-commit-msg` at `<repo>/.git/hooks/prepare-commit-msg`.
- The hook is a small shell script that calls back into the `tickets_please` binary: `tickets_please commit-hook run "$@"`.
- `commit-hook run` logic:
  1. Read the commit-msg file passed as $1.
  2. If the message already contains `tickets-please/NNN`, do nothing.
  3. Query the local store (or local MCP) for tickets currently in `in_progress` assigned to the active agent (identified via the bound project + agent key from `~/.tickets_please/`).
  4. If exactly one in-progress ticket, append a footer: `tickets-please/NNN` on its own line.
  5. If 0 or >1, do nothing (don't guess); print a tip to stderr.
- The hook NEVER overwrites; only appends. If an existing `prepare-commit-msg` hook is present, install warns and refuses unless `--force-append` (in which case it wraps the existing hook).
- `tickets_please commit-hook uninstall` removes the hook (only if it's our hook).
- `tickets_please commit-hook status` reports installed/not.
- Tests cover: append logic, 0/1/many in-progress cases, idempotency on re-run, non-clobbering of existing hooks.

## Hints

- Existing tickets_please mutations already commit via `internal/store/git.go` — that's the in-repo "system commit" path, not the user's feature commits. This hook targets the user's feature commits only.
- Document the install command prominently in README and as a first-run suggestion on the project overview page.
