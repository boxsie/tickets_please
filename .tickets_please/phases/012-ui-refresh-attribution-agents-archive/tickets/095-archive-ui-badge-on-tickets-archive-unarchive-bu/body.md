Archive is fully backend-implemented (search-feedback-archival phase). The UI knows nothing about it. Fix that.

## Acceptance

- Archived badge added to:
  - Ticket-detail header (next to status badge).
  - Every ticket reference card/list/wave row/search hit (small "archived" pill).
- Ticket-detail page-actions add an "Archive" button for non-archived tickets (any column) and an "Unarchive" button for archived tickets.
- Each action opens a modal requiring a comment (mirrors the existing move/complete modal pattern), then POSTs to a new `POST /tickets/{id}/archive` / `POST /tickets/{id}/unarchive` route that wraps the existing service methods.
- Archive/unarchive is reflected in the `system_archive` / `system_unarchive` comment kinds that already render in the thread.
- Done + archived is a valid state — buttons remain available on done tickets (only completion-field edits are frozen).
- Tests cover: handlers, modal renders, archived badge appears on the detail and ticket cards.

## Hints

- The "Frozen actions" partial for done tickets needs Archive/Unarchive added — frozen tickets can still be archived/unarchived.
- Visual archived treatment for cards: 60% opacity + strikethrough title.
