## What

Replace the always-on right-rail action cards on `/tickets/{id}` with `<dialog>` modals triggered from header buttons. Stretch the body section to span full content width.

## Why

The current layout (input.css:961, `pages/tickets/detail.tmpl:65-110`) reserves a fixed 22rem column for Move / Complete / Reassign-phase forms. Users mostly read tickets; the forms are noise the rest of the time and they squeeze long markdown / code blocks / dependency lists.

## Implementation sketch

1. **`pages/tickets/detail.tmpl`** — for the non-frozen branch:
   - Drop the `.ticket-grid` 2-column wrapper. The body and sections live directly under `.ticket-detail`.
   - In the header `.page-actions`, add buttons that open dialogs: `Complete` (primary), `Move`, and `Reassign phase` (when `$b.Phases` non-empty). Keep the existing `Edit ticket` link.
   - At the bottom of the page (or just above `</section>`), render three `<dialog>` elements containing `move_form.tmpl`, `complete_form.tmpl`, `assign_phase_form.tmpl`. Each dialog gets a header with title + close `<form method="dialog"><button>×</button></form>`.

2. **Inline JS** (small `<script>` block, same style as the sidebar picker — past learning, ticket 89efc4cf):
   - On click of a `[data-dialog]` button, find the dialog by id and `.showModal()`.
   - Click on the dialog backdrop (event target == dialog) closes it.

3. **CSS in `input.css`**:
   - New `dialog.modal` styles: dark panel matching `--bg-elev`, `--line` border, padding, max-width ~36rem, `border-radius`, `box-shadow`. `::backdrop` gets a translucent dark overlay.
   - `.modal-header` (title + close button row), `.modal-body` (form container).
   - Drop / repurpose `.ticket-grid` rule that hardcodes `22rem`. Keep the frozen branch's grid usage if we want — but simpler to drop the grid entirely; both branches share `.ticket-main`-style content full-width. (Frozen branch already only uses `.ticket-main`, so collapsing the wrapper is harmless.)
   - Tweak `.ticket-detail .card` widths if needed; currently each card is `width: 100%` so removal of the grid just gives them more room.

4. **`tailwind.config.js`** — safelist `modal`, `modal-header`, `modal-body`, `dialog` if needed.

5. **Frozen path unchanged** — `frozen_actions.tmpl` keeps rendering the disabled-button row.

## Hard rules / risks

- Dialog forms are still server-POST forms. Submit → 303 redirect → page reload → dialog gone. No client-side partial swap needed.
- Validation errors today re-render the page; that means a closed dialog after a failed submit. Acceptable for v1; the page will show the inline error via existing flash/error rendering, and the user re-opens the dialog to retry.
- `<dialog>` is supported in all evergreen browsers (Chrome 37+, Firefox 98+, Safari 15.4+). No fallback needed.

## Verification

1. `make tailwind` (or whatever the CSS rebuild command is) — confirm dist/app.css contains the new `.modal` rules.
2. `tickets_please serve --dev`, browse to a non-done ticket. Click each header button — dialog opens, form renders inside it, form fields are interactive. Submit; page reloads with the expected state (column moved, phase reassigned, etc.).
3. Submit `Complete` from inside the dialog with valid contents; ticket goes to `done` and the page now renders the frozen branch.
4. Click outside the dialog panel and press ESC — both close it.
5. Browse to a done (frozen) ticket — old disabled-button row still shows; no dialogs mounted.
6. Visual check: ticket body, dependencies card, and comment thread span full content width.
7. `go test ./internal/web/...` — existing tests stay green (the change is template-only on the structural side; handlers untouched).
