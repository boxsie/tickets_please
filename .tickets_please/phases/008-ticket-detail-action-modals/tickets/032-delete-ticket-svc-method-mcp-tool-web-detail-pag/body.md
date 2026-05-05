## Goal

Add a delete-ticket capability across the three layers, scoped to non-`done` tickets only — preserving SPEC's "completion is sacred" rule.

## Scope

### 1. svc layer — `Service.DeleteTicket(ctx, id) error`

Mirror `DeletePhase` / `DeleteProject` shape:
- `requireSession`; resolve project store via `hostStoreForTicket` (caller passes opaque ticket id, like `GetTicket`).
- Acquire `lp.Lock` exclusive.
- Refuse if `ticket.Column == ColumnDone` → `ErrFailedPrecondition`, message points at SPEC's no-reopen rule.
- Refuse if any other ticket lists this one in `DependsOn` → `ErrFailedPrecondition`, list dependents in the message so the caller can clear them.
- `Worker.Flush(ctx)` to drain pending embed jobs that might re-create a sidecar.
- `BeginOp` → `RemovePath(ticketDirRel)` (relative path from `findTicketDir`) → `Commit` under per-project flock.
- After commit success: drop entry from `lp.Tickets` + `lp.Comments`; `TicketsIdx.Delete(ticketID)`, `LearningsIdx.Delete(ticketID)` (defensive), and walk the cached comment slice to `CommentsIdx.Delete(commentID)` for each.
- No deletion comment / tombstone — the audit trail comes from the auto-commit only (caption `delete ticket <slug>/<NNN>`).

### 2. MCP tool — `delete_ticket`

`delete_ticket(ticket_id)` → wraps `Service.DeleteTicket`. Description hammers home that it's irreversible and refuses on `done`.

### 3. Web — delete button on ticket detail

- New trigger button in the `pages/tickets/detail.tmpl` header action group: `Delete` (only when ticket is not done, matching how Move/Complete/Reassign are gated). Uses the same `data-dialog` modal pattern from ticket 031.
- Confirmation modal with the ticket title spelled out and a "Delete forever" submit; CSRF-protected POST to `/tickets/{id}/delete?slug={proj}`.
- New handler `handleTicketDelete` that calls `svc.DeleteTicket`, on success redirects to `/p/{slug}/board` with a flash message; on `ErrFailedPrecondition` (done / dependents) returns a clear inline error.
- New route entry in `router.go`. CSS for the modal reuses the existing `.modal` styles.

## Hard rules / SPEC alignment

- Never deletable on `done` — preserves "completion is sacred". Documented in tool description + UI tooltip.
- Refuses dependents — no dangling `DependsOn` refs.
- No tombstone needed: git auto-commit already records the removal.
- Comments-immutability rule survives: this deletes the ticket *and its comments together*, not individual comments.

## Out of scope

- Bulk delete.
- Soft-delete / undo.
- Web modal lazy-load via htmx (form is a single button, doesn't need it).

## Critical files

- `internal/svc/tickets.go` — new `DeleteTicket` method.
- `internal/svc/tickets_test.go` — happy path, refuses-done, refuses-dependents.
- `internal/mcptools/tools.go` — new `delete_ticket` tool registration + handler.
- `internal/web/router.go` — `POST /tickets/{id}/delete`.
- `internal/web/handlers_tickets.go` — new `handleTicketDelete`.
- `internal/web/templates/pages/tickets/detail.tmpl` — header button + dialog.
- `internal/web/static/_src/input.css` (+ tailwind safelist if needed) — minor styling for the danger-confirm modal if reusing `.modal-danger` doesn't exist yet.

## Verification

- `go test ./internal/{svc,mcptools,web}/...` green.
- Manually: open an active ticket, click Delete, confirm — redirected to board, ticket gone. Open a `done` ticket — no Delete button. Try via MCP on a `done` ticket — clear error. Create A→B (B depends on A), try to delete A — refused with B's title in the error.
