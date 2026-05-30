First user-visible surface from this phase. Below the ticket body, show every commit that referenced this ticket.

## Acceptance

- New "Commits" card on ticket detail (only rendered when the ticket has any indexed commits).
- Header: "{N} commits • +{insertions} / -{deletions} across {days_span} days".
- Each row: short-SHA (copyable), commit subject (truncated to 80ch), author, relative time, files-changed count, +/- numbers. Subject links to `/commits/{sha}?slug={project}` rendering an in-app diff view (HTML rendering of `git show` output, no syntax-highlight dependency required v1).
- Empty state hidden — no card if no commits.
- Index is refreshed on render (rate-limited per [[commit-indexer-reverse-index]]).
- Tests cover the handler integration with `CommitsForTicket` and the empty-state behaviour.

## Hints

- The in-app diff view is its own small thing — render `git show --stat` first, with per-file diffs in collapsible `<details>` sections to keep large diffs reasonable.
- For GitHub-provider projects, also link out to the commit on GitHub.com next to the in-app link.
