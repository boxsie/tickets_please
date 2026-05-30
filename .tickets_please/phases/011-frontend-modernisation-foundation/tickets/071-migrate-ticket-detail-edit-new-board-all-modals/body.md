Port the ticket surface and every modal it owns. Largest single-page migration; do it last among the page ports so the component library is mature.

## Acceptance

- Ported to `internal/web/components/pages/tickets/`: `detail.tmpl`, `edit.tmpl`, `new.tmpl`, `board.tmpl` (yes, port it — Phase 2 deletes it, but during the cutover both stacks must agree).
- Ported partials: `ticket_card.tmpl`, `comment.tmpl`, `comments_thread.tmpl`, `comment_form.tmpl`, `move_form.tmpl`, `complete_form.tmpl`, `frozen_actions.tmpl`.
- All four modals (move, complete, assign-phase, delete) re-rendered using the new `Modal` component.
- Markdown rendering (`markdown` template func) preserved — keep the helper, just expose it to templ via a method on the renderer context or a small wrapper func.
- Dialog open/close JS extracted from inline `&lt;script&gt;` into a single `internal/web/static/dialogs.js` (referenced from base layout). Same data-attribute API (`data-dialog`, `data-dialog-close`).
- All forms (move/complete/assign/delete/edit/new) submit to the same POST routes with the same field names — handlers untouched.
- Smoke tests + `handlers_tickets_test.go` pass.

## Hints

- The ticket-detail metadata block changes substantially in Phase 2 ([[ticket-detail-metadata-block]]); for this ticket, just port the existing thin header verbatim.
- Use `internal/web/components/ui/Modal` for all four dialogs — same shell, different bodies.
