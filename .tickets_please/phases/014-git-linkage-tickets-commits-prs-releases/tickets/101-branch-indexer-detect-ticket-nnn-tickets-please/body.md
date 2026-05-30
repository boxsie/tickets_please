Surface "active branches" per ticket so an in-progress ticket shows where the work is happening, especially useful when multiple agents collaborate on one ticket.

## Acceptance

- `Indexer.RefreshBranches(ctx, project)` lists branches via the provider, regex-matches `^(ticket|tickets-please)/(\d{3})(-.*)?$`, maps ticket-number → branch.
- Index entry per branch: `{name, head_sha, ahead_of_default, behind_default, last_pushed_at}` (ahead/behind only computable on github provider — local provider populates from `git rev-list --count`).
- `svc.BranchesForTicket(ctx, ticketID) ([]Branch, error)`.
- Branch index lives alongside commits in `git-index.yaml` under a `branches:` block.
- Tests cover the regex (including unmatched, edge cases like 2-digit padding), and the index update.

## Hints

- The regex captures the number; the index uses the number to find the ticket (numbers are globally unique per project).
- For local provider, `git for-each-ref refs/heads/` returns the data needed in one call.
