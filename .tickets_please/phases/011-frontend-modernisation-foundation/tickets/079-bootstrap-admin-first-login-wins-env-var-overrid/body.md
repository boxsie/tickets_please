Need a story for first-run: how does the very first user become owner of every existing project, and how does an admin recover from a lock-out?

## Acceptance

- `TICKETS_PLEASE_BOOTSTRAP_ADMIN` env var: if set, the matching OAuth identity (e.g. `github:boxsie` or `google:dan@example.com`) is auto-granted `owner` of every existing project on login.
- If env var is unset AND the users store is empty, the first user to complete OAuth becomes `owner` of every existing project (logged as a one-time bootstrap event).
- A `tickets_please grant-owner --user-id ... --project ...` CLI subcommand for recovery (writes directly to the membership store, bypassing HTTP).
- `tickets_please list-users` and `tickets_please list-memberships --project ...` CLI subcommands for audit.
- Tests cover: empty users store + login → admin promoted; non-empty store + no env → new user has zero memberships; env-set login → owner of all.

## Hints

- Existing projects need a backfill path — on first admin promotion, iterate the project list and grant.
- CLI subcommands go under `cmd/tickets_please/` as sibling files to the `serve` command.
