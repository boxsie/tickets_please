User said it directly: "the board page is useless, nothing fits in and its not readable, we dont need a trello board". Phases→waves is the actual workflow.

## Acceptance

- `handleBoard` removed; `GET /p/{slug}/board` returns `302 Found` to `/p/{slug}/phases` (preserve external links).
- Sidebar's "Board" link → "Phases" (in `internal/web/components/layout/sidebar.templ`).
- Project overview's "Open board" primary CTA → "Browse phases".
- Board template files deleted (`board.tmpl` + the templ port from W1 if it landed).
- `handlers_tickets.go` cleaned up of board-only helpers (board columns assembly etc.); same with any board-only types in svc.
- Tests for the redirect; remove board-page tests.

## Hints

- Keep the 302 forever — it's cheap insurance against stale links in bookmarks, agent memory, comments.
