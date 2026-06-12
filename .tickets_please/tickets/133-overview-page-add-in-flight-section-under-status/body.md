Add a new section to the **project overview page** that surfaces the work actually in flight right now: tickets in `in_progress` and `testing`, each shown with a link. Project-wide — no phase scoping.

## Where
- Page: `internal/web/components/pages/projects/detail.templ` (the project overview/dashboard).
- Placement: a new `@ui.Card` inserted **directly under the "Status distribution" card** (currently `detail.templ:67-90`), before the "Phases" card (`:91`).

## What it shows
- Tickets in the `in_progress` and `testing` columns only — the things being actively worked, across the whole project.
- Each row: ticket title linking to the ticket (reuse the existing pattern `/tickets/<id>?slug=<slug>`), plus a column badge (`badge badge-<column>`) like the "Ready to pick up" / "Recent activity" cards already use. Reuse `.ticket-list` markup for consistency.

## What it deliberately excludes (and why)
- **No `todo`** — that's effectively the whole site, and the "Ready to pick up" card on this same page already surfaces unblocked todo work. Don't duplicate it.
- **No `done`** — already covered by the "Recent activity" panel at the bottom (`detail.templ:122-141`, the `dashboard-grid`).

So this card is the missing middle: between "what's queued" (Ready) and "what just landed" (Recent activity), show "what's in hand."

## Plumbing
- `internal/web/components/pages/projects/data.go` — add an `InFlightTickets` field to the detail props (mirror `ReadyTickets`).
- `internal/web/handlers_projects.go` — populate it. There's already a service path that lists/filters tickets by column (the `columns` filter on list); fetch `in_progress` + `testing` the same way `ReadyTickets` is built.
- `detail.templ` — render the new card (guard on non-empty, with a friendly empty state like the others).
- Regenerate templ: `templ generate` (produces `detail_templ.go`).
- Tests: extend `internal/web/handlers_projects_test.go` to assert the card lists in_progress/testing tickets and excludes todo/done.

## Acceptance
- Overview page shows an "In flight" (name TBD) card under Status distribution listing every `in_progress` + `testing` ticket with a working link and column badge.
- A `todo` ticket does **not** appear there; a `done` ticket does **not** appear there.
- Empty state renders cleanly when nothing is in flight.
