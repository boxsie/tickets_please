Close the loop: when a commit referencing ticket N is pushed (or a PR opens, or a release ships), the ticket detail page updates live.

## Acceptance

- Indexer emits events into the SSE hub after each refresh:
  - `CommitLinked{ticket_id, commit_sha, author, message}` on `ticket:{id}`.
  - `PRLinked{ticket_id, pr_number, state}` on `ticket:{id}`.
  - `ReleaseShipped{ticket_id, release_tag}` on `ticket:{id}` AND `project:{id}` (since the releases page also updates).
- Ticket detail page subscribes (via Phase 1 W3 SSE hub) to these and patches:
  - New commit row prepended to commits card; live counter ("+1 commit just landed by Claude") in a transient toast.
  - PR state badge updates in place; new PR added if not present.
  - "Shipped in v1.4.0" pill appears next to status badge.
- Project overview's "Recent activity" gets git events interleaved with ticket-move events (chronological).
- Tests cover: event publish on indexer refresh; correct topic; templ patch render.

## Hints

- The toast for "commit landed" is a delightful microinteraction — slide-in, brief, auto-dismiss.
- Don't double-publish if the indexer refresh races with itself; dedupe by SHA at the hub layer or use idempotent publish.
