## Phase: Phases & waves UX

The current web UI treats phases and waves as two unrelated pages:

- `/p/{slug}/phases` is a flat table of phases with name + ticket counts.
- `/p/{slug}/waves` is a flat list of waves, each dumping every ticket in that wave with a `phase {id}` aside as the only hint that phases exist.

Functionally that works, but it hides the actual relationship: **phases group work, waves order it across the project**. A user looking at "phase X" can't see how its tickets break down by wave without going to the waves page; a user looking at "wave 2" can't see how it spans phases without reading the muted phase ID on each ticket. The pages need to teach that interconnection on first read.

## Decided design

Two complementary views, one per page, no shared template:

1. **`/phases` becomes the drill-down view.** Each phase row expands (HTML `<details>`) to reveal that phase's waves, with the ticket list per wave. Default-collapsed so the index stays compact; opening a row gives you the full per-wave breakdown without leaving the page.
2. **`/waves` becomes the cross-cutting view.** Rebuild as a wave × phase matrix (rows = waves, columns = phases, cells = ticket counts plus a status dot mini-distribution). Cells link into the relevant phase detail. Phase-less tickets get an "Unphased" column. Wave 0 (unassigned) sorts last on its own row, same convention the data already uses.

The two pages stop overlapping: one answers "what's in phase X?" with wave-detail; the other answers "how does wave N coordinate across the project?".

## Scope

### Ticket 1 — Phases page: collapsible wave breakdown per phase

- Enrich `handlePhasesIndex` to compute, per phase, a `[]waveSection` (wave number → tickets in that phase × wave). Reuse the existing in-handler bucketing pattern from `handleWaves`.
- Restructure `pages/phases/index.tmpl`: each phase row becomes a `<details>` with a summary header (phase name, counts, summary preview) and an expanded body listing each wave with its tickets (status dot + title + link).
- Default `<details>` state: collapsed.
- Tickets in waves render with the same status-dot pattern as the dashboard's recent-activity list (`.dot-{column}` classes; safelisted already).

### Ticket 2 — Waves page: cross-phase matrix view

- Enrich `handleWaves` to also compute per-(wave, phase) buckets keyed off `(wave, phase_id_or_nil)`.
- Rebuild `pages/phases/waves.tmpl` as a matrix:
  - Header row: empty corner + one column per phase (sorted by phase number) + an "Unphased" column on the right.
  - Body rows: one per wave (sorted ascending, wave 0 last). Row header = wave number. Each cell = ticket count + tiny status-dot distribution (todo/in_progress/testing/done). Empty cells render as a muted `—`.
- Each non-empty cell links to `/p/{slug}/phases/{phase_slug}?wave={n}` (note: phase detail does not currently filter by wave; that's a future enhancement, OK if the link just lands on the phase for now).
- Below the matrix, retain a fallback "expanded wave" view (the current flat-list layout) for users who want every ticket title visible at once. Use a tab or `<details>` toggle, matter of preference.
- If the project has zero phases, fall back to the current flat-list layout (matrix degenerates to a 1-column table that's worse than the existing UX).

## Hard rules

- The matrix must not silently drop tickets. If `len(matrix.cells) != len(tickets)` (e.g. a phase ID points to nothing), surface a debug-banner; don't lose data.
- Wave 0 (unassigned) and "Unphased" tickets are first-class, not aside text. Both get their own row/column.
- No new JS framework. The collapsible phase rows use native `<details>`; the matrix is a plain HTML `<table>` styled with CSS Grid or table-layout fixed.

## Verification

- After both tickets:
  - Visit `/p/tickets-please/phases`. Each phase row collapses by default; clicking a row reveals waves with ticket lists.
  - Visit `/p/tickets-please/waves`. See a wave × phase matrix; each non-empty cell shows ticket counts + dots; cell click navigates into the phase.
  - Visit a project with no phases — the waves page falls back to the existing flat-list layout, no crash.
- `go test ./internal/{svc,web}/...` green; new tests assert: (a) phases-index handler enriches with wave breakdown; (b) waves-page handler computes per-(wave, phase) cells and never drops tickets.
- Playwright walkthrough updated with screenshots of both pages.

## Critical files

- `/home/dan/code/tickets_please/internal/web/handlers_phases.go` — `handlePhasesIndex` (~line 38), `handleWaves` (~line 347).
- `/home/dan/code/tickets_please/internal/web/templates/pages/phases/index.tmpl` — flat table to rebuild.
- `/home/dan/code/tickets_please/internal/web/templates/pages/phases/waves.tmpl` — flat dump to replace.
- `/home/dan/code/tickets_please/internal/svc/waves.go` — `ListWaves` only returns wave summaries scoped to one phase (or phase-less when nil); the cross-phase view must keep using `ListTickets` + in-handler bucketing.
- `/home/dan/code/tickets_please/internal/web/static/_src/input.css` + `tailwind.config.js` — new classes for matrix cells, status-dot mini-distributions; safelist any pattern-matched names.

## Out of scope

- Per-wave filter on phase detail (the matrix cells link to phase detail only; a `?wave=N` filter is a follow-up).
- Drag-and-drop ticket-between-waves UX (button moves only, same as the rest of the UI).
- Wave creation/edit UI (waves are integers on tickets — they "exist" by being referenced; no separate CRUD).
- Cross-project wave aggregation (single-project view; cross-project search already covers the discovery path).

## Notes

- Two tickets, parallelizable after this phase is created. Ticket 1 (phases page) is the simpler change; Ticket 2 (matrix) is the bigger lift.
- Past learning from `e749b038` warns: `ListWaves` with `nil` phase only returns phase-less wave summaries — the existing `handleWaves` already avoids this trap by using `ListTickets` directly. Both new code paths must keep that pattern.
- Past learning from `89efc4cf` confirms `<details>` is the right primitive for collapsible regions in the no-JS world.
