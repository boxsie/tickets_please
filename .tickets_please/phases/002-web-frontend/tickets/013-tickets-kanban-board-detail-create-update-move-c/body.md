## Goal

The headline UX. A four-column Kanban board per project (and per phase), a ticket detail view, create form, in-place edit, comment-required move, and a structured completion form. Hard rules from the SPEC are enforced server-side and surfaced inline.

## Why

Tickets are the unit humans want to act on. This ticket is what makes the UI useful for day-to-day work. It's also the largest single surface in the phase — recommend splitting into 5a/5b along the read/write seam (see "Splitting" below).

## Scope

### Routes (handlers/tickets.go)

| Method | Path | Purpose | Service call |
|--------|------|---------|--------------|
| GET  | `/p/{slug}/board`                         | Kanban: 4 columns × N cards. Optional `?phase=<phase-slug>` to scope. | `svc.ListTickets(slug, phase?)` |
| GET  | `/p/{slug}/tickets/new`                   | Create form (title, body, phase select, depends_on multi-select, parallelizable_with, wave) | `svc.ListPhases`, `svc.ListTickets` (for dependency picker) |
| POST | `/p/{slug}/tickets`                       | Create ticket | `svc.CreateTicket` |
| GET  | `/tickets/{id}`                           | Detail page | `svc.GetTicket` (+ `svc.ListComments` from ticket 6 once landed) |
| GET  | `/tickets/{id}/edit`                      | Edit form (title, body, depends_on, parallelizable_with, wave) | `svc.GetTicket` |
| POST | `/tickets/{id}`                           | Update ticket | `svc.UpdateTicket` |
| POST | `/tickets/{id}/move`                      | Column move (target_column + required comment) | `svc.MoveTicket(id, target, comment)` |
| POST | `/tickets/{id}/complete`                  | Completion (target=`done` only path) | `svc.CompleteTicket(id, testing_evidence, work_summary, learnings, comment?)` |

All ticket-mutation routes accept an optional `?slug=<project>` query hint (see Gotchas) so handlers can skip `hostStoreForTicket`'s O(mounts) walk when the slug is known from the page that originated the action (board, detail).

### Templates (under `internal/web/templates/pages/tickets/`)

- `board.tmpl` — four columns (`todo`, `in_progress`, `testing`, `done`); each column is a `<section>` with a `<header>` (column name, count) and a `<ul>` of `partials/ticket_card.tmpl`. `done` column visually muted (frozen).
- `new.tmpl` — title (req), body (markdown textarea), phase select, depends_on multi-select with type-ahead via htmx (`hx-get="/p/{slug}/tickets?ready=&col=todo,in_progress&q="`), parallelizable_with similar, wave (number input, default 0).
- `detail.tmpl` — header (title, phase breadcrumb, column badge, agent attribution, dates), body markdown rendered, depends/blocks lists, action panel (move/complete/edit/assign-phase), comments section (ticket 6 fills in the `<section id="comments">`).
- `edit.tmpl` — analogous to new, pre-filled.
- Partials:
  - `ticket_card.tmpl` (used on board, search results, dependency picker; click → `/tickets/{id}?slug={slug}`).
  - `move_form.tmpl` — column dropdown (`todo`/`in_progress`/`testing`) + required `<textarea name="comment" minlength=1 required>`. If user picks `done`, JS swaps to the completion form (or use plain `<details>` to avoid JS).
  - `complete_form.tmpl` — three required textareas (`testing_evidence`, `work_summary`, `learnings`, each `minlength=10`) + optional comment. Submit → `POST /tickets/{id}/complete`.
  - `error.tmpl` (extended from ticket 1) — surfaces Service errors inline next to the form.
  - `frozen_actions.tmpl` — disabled action buttons with `<title>` tooltip explaining why on `done` tickets.

### htmx interactions

- Board column → ticket card move: out of scope for v1 (no DnD). Buttons on the card open the move form in a `<details>` element next to it.
- Move success → swap the ticket card into its new column via response `HX-Trigger: ticket-moved` + per-column hx-listener that re-fetches the affected columns. (Cheaper alt: just full-page reload after move; choose the simpler one if htmx swap proves fiddly.)
- Move-to-`done` → handler returns 422 with `partials/error.tmpl` saying "use the Complete action; `done` is sacred". UI's move dropdown should not include `done` as an option to begin with.
- Complete success → redirect to detail page (now showing frozen state).

### Dependency rendering

- On detail page show `Depends on:` and `Blocks:` lists with title + column badge per ticket.
- On board, dim cards whose `depends_on` includes a non-`done` ticket; tooltip "blocked by: <list>".
- Use Service's `ready_only` filter logic for the dimming if it's exposed; else compute client-side by joining the visible tickets.

## Hard rules to enforce + surface

1. **Move requires comment** — server enforces (`MoveTicket` errors on empty); UI form has `required` + 422 partial.
2. **`done` only via `complete_ticket`** — server rejects `MoveTicket(target=done)`; UI move dropdown omits `done`; the "Complete" button on the action panel routes to the completion form.
3. **Completion fields ≥10 chars each** — server enforces; UI has `minlength=10` on each textarea. Validation lives server-side; client minlength is a hint.
4. **`done` tickets are frozen** — UI renders `frozen_actions.tmpl` (all buttons disabled with explanatory tooltip); detail page makes the body read-only.

## Splitting (recommended for the implementing agent)

Split into two PRs that merge cleanly because URLs don't collide:

- **5a (read)**: `GET /p/{slug}/board`, `GET /p/{slug}/tickets/new`, `POST /p/{slug}/tickets`, `GET /tickets/{id}`. Templates: board, new, detail (read-only), card partial.
- **5b (mutations)**: `GET /tickets/{id}/edit`, `POST /tickets/{id}`, `POST /tickets/{id}/move`, `POST /tickets/{id}/complete`. Templates: edit, move_form, complete_form, frozen_actions.

5a unblocks ticket 6 (comments), so prioritise it.

## Key references

- `internal/svc/service.go:CreateTicket,GetTicket,ListTickets,UpdateTicket,MoveTicket,CompleteTicket`.
- `internal/svc/service.go:hostStoreForTicket(id)` (~line 534) — O(mounts) walk; pass `?slug=` from the originating page to bypass.
- `internal/mcptools/tools.go` — `move_ticket`, `complete_ticket` handlers for the validation + error-shaping pattern.
- SPEC.md "Hard rules" section — the four invariants this UI surfaces.

## Gotchas

1. **`hostStoreForTicket` is O(active mounts)** — for individual mutations triggered from the board/detail (where slug is known), pass `?slug={slug}` and call `ResolveProjectStore(slug)` directly. Fall back to `hostStoreForTicket(id)` only when slug isn't known (e.g. deep links from search).
2. **Embedding worker is async** — handlers return immediately after Service writes; don't block on the embed pipeline for the move/complete response.
3. **Body markdown can contain HTML** — render via the same goldmark + sanitiser pipeline ticket 3 sets up. NEVER `template.HTML` raw user input.
4. **Multi-select form encoding** — `depends_on` and `parallelizable_with` are repeating form fields (`name="depends_on"` per checkbox). Use `r.Form["depends_on"]` to slice them.
5. **Wave 0 is "unassigned"** — surface that in the wave input help text.
6. **Optimistic UI, not eventual** — after a move, the response payload includes the updated ticket so the card re-renders accurately even if the server is slow to fan out. Don't trust client-side state.

## Verification

- Manual flow:
  1. From `/p/tickets-please/board`, click `[+] new ticket`. Fill in title `test-ticket`, body, leave wave 0.
  2. After POST, ticket appears in `todo` column.
  3. Click ticket → detail page renders body markdown; action panel visible.
  4. Move → `in_progress` with comment `starting`. Card moves columns.
  5. Try move to `done` → blocked, redirected to completion form. Fill in three required fields, submit. Card moves to `done` column, frozen.
  6. Try edit a `done` ticket → all buttons disabled with tooltip; URL `/tickets/{id}/edit` returns 422 inline error.
  7. Move to `in_progress` with empty comment → 422 inline.
- MCP cross-check: `mcp__tickets_please__list_tickets` matches UI columns; `mcp__tickets_please__get_ticket` shows the audit-trail comments.
- Open the same project in two browser windows; move in one; confirm the other shows stale state until reload (real-time push is out of scope).
- `go test ./internal/web/handlers/...` for the move/complete validation paths and the slug-hint short-circuit.

## Out of scope

- Drag-and-drop reordering (deferred polish).
- Real-time push updates (no SSE/WebSocket — manual refresh or board-level htmx polling at v2).
- Bulk moves / multi-select on the board.
- Comment thread (ticket 6 — the `<section id="comments">` slot is reserved here but rendered there).
- Search (ticket 7).

## Notes

- Heaviest ticket in the phase. If you split into 5a/5b, file 5b as a follow-up ticket via `create_ticket` once 5a is in `testing` so other agents can pick it up in parallel.
- Coordinate the `?slug=` hint convention with ticket 4 (which adds `/tickets/{id}/assign-phase`) so all ticket-mutation URLs share the same query-string convention.
- The card hover/active/dim styling is the most visible polish detail. Lean on Tailwind utilities; commit any new classes to `_src/input.css` and regen.
