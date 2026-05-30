Walk commits via the provider, extract `tickets-please/NNN` refs from messages, persist a per-project reverse index that maps ticket → commits.

## Acceptance

- `internal/git/refparser.go`: extracts `tickets-please/NNN` (and aliases `tp/NNN`, `tickets_please/NNN`) from a commit message. Supports multiple refs per commit.
- `internal/git/indexer.go`: `Indexer.Refresh(ctx, project)` walks commits via the provider since the last indexed SHA, parses refs, upserts into a per-project YAML index at `.tickets_please/git-index.yaml`:
  ```yaml
  last_indexed_sha: abc123
  last_indexed_at: 2026-...
  commits:
    "abc123": { sha, author, author_email, message, parents, files_changed_count, insertions, deletions, committed_at, tickets: [063, 064] }
  tickets:
    "063": [abc123, def456]
  ```
- Index is rebuildable: `Indexer.Rebuild(ctx, project)` walks from scratch.
- `svc.CommitsForTicket(ctx, ticketID) ([]*Commit, error)` reads the index, returns commits for the ticket sorted by date desc.
- Auto-refresh: on remote-with-webhook setup, the webhook handler from [[github-webhook-receiver]] triggers a partial refresh; on local setup, refresh on each ticket-detail render (bounded by a per-project 60s rate limit).
- Tests cover parser (every alias + edge cases like multiple refs), indexer (upsert idempotent, rebuild from scratch), `CommitsForTicket` ordering.

## Hints

- Ticket-ref format: parser matches the slug+number form `<project-slug>/NNN` (e.g. `tickets-please/063`). Project slug is known to the indexer; no other-project refs leak in.
- `files_changed_count`/`insertions`/`deletions` from `git show --numstat` (local) or PR stats endpoint (github).
