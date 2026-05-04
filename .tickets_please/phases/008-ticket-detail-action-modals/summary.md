## Phase: Ticket detail — action modals

### The current layout

`pages/tickets/detail.tmpl` renders a 2-column grid: a `.ticket-main` body on the left and a sticky `.ticket-side` rail on the right that always carries three action cards — **Move**, **Complete**, and (when the project has phases) **Reassign phase**. The grid is `minmax(0, 1fr) 22rem` (input.css:961-972), which steals 22rem off every viewport for forms that aren't being used 99% of the time.

### The problem

User feedback: the action forms on the right shouldn't be on the page the whole time — most reads of a ticket are reads, not writes. Permanently-rendered Move + Complete + Reassign chrome:

1. **Squeezes the body** of the ticket — the part the user actually came to read — into a narrow column. Long markdown bodies, code blocks, and dependency lists wrap aggressively when 22rem of horizontal space is reserved next to them.
2. **Adds visual noise.** Three forms with labels, textareas, and submit buttons sit there even when the user is just scanning the ticket.
3. **Pushes the comment thread off-screen** on shorter viewports because the sticky side rail is taller than the body sections it sits next to.

### Decided design

Drop the right rail entirely. Promote the three header buttons (Edit, Move, Complete, Reassign-phase) so each opens a modal dialog containing the corresponding form. The body section spans the full content width.

**HTML primitive: native `<dialog>`.** The same reasoning that made `<details>` the right pick for collapsible regions and the project picker (past learning, ticket 89efc4cf) applies here: `<dialog>` is the no-JS-framework primitive for modals. `dialog.showModal()` handles backdrop, focus trap, ESC-to-close, click-outside-close (with a few lines of inline script). Open/close is native; we keep the existing server-rendered forms unchanged inside the dialog body.

**Form submission stays the same.** Move, Complete, and Reassign-phase already POST to existing handlers (`/tickets/{id}/move`, `/complete`, `/assign-phase`). The forms move from `.ticket-side` cards into `<dialog>` containers, but the action URLs, CSRF tokens, and validation behavior don't change. On submit, the existing 303 redirect (or htmx swap, where applicable) tears the dialog down by navigating away — no extra JS needed for the happy path.

**Frozen tickets** already short-circuit to disabled buttons via `partials/frozen_actions.tmpl`. We keep that; modals only mount for non-frozen tickets.

**Sticky behavior on the buttons.** The header `.page-actions` already pins under the page header. Buttons that open modals should be visually grouped: primary action `Complete`, secondary `Move`, tertiary `Reassign phase`, and the existing `Edit ticket` link.

### Out of scope

- Replacing the move/complete/assign-phase forms with htmx-driven dialog content (they're already small and inline-rendered; a follow-up could lazy-load the dialog body via htmx, but it's not a current pain point).
- Drag-and-drop column moves from board view (separate phase if ever).
- Modals on any other page (project / phase pages keep their current shape).
- Replacing the existing browser `confirm()` pattern (delete project) — that pattern is fine for destructive single-button confirmations; modals here host multi-field forms.

### Critical files

- `internal/web/templates/pages/tickets/detail.tmpl` — the grid + side rail to dismantle.
- `internal/web/templates/partials/{move_form,complete_form,assign_phase_form}.tmpl` — forms move inside dialogs.
- `internal/web/static/_src/input.css` — drop unused `.ticket-side` styles, widen `.ticket-main`, add `<dialog>` styling (backdrop, panel chrome).
- `internal/web/static/_src/tailwind.config.js` — safelist any new dialog-related classes.

### Verification

- Open a ticket detail page in both states (active, frozen). Active: see Move / Complete / Reassign-phase buttons in the header; clicking each opens the corresponding dialog with the existing form. Frozen: existing disabled-button row still renders.
- Submit each form from inside its dialog — the existing handler accepts the POST, the page reloads (or 303s back), and the resulting state is correct (move comment recorded, completion fields persisted, phase reassignment applied).
- Body content spans the full width — long markdown, code blocks, and dependency lists no longer wrap to a 22rem-narrowed column.
- Existing playwright walkthrough still passes; one new screenshot of an open dialog is welcome but not gating.

### Notes

- Single-ticket phase. Coherent change, ~one PR's worth of work.
- Reuse `<dialog>` rather than reimplement focus/backdrop in JS. The few lines of JS we add (open by id, click-outside-close) match the existing inline-script style used by the sidebar picker.
