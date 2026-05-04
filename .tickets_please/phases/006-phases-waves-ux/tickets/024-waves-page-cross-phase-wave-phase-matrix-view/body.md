## Goal

Replace the flat data-dump on `/p/{slug}/waves` with a wave × phase matrix that surfaces how a wave coordinates across the project. Rows = waves; columns = phases; cells = ticket count + tiny status-dot distribution. Phase-less tickets get an "Unphased" column. Wave 0 (unassigned) sorts last.

Below the matrix, retain a fallback "expanded view" (the current per-wave flat list) for users who want every ticket title visible at once.

## Scope

### Handler (`internal/web/handlers_phases.go`)

- `handleWaves` (~line 347) currently buckets tickets by wave only. Add a second dimension: also bucket by `(wave, phase_id_or_nil)`.
- New page-data shape:
  ```go
  type wavesPageData struct {
      Project *domain.Project
      Phases  []*domain.Phase   // ordered by Number, for matrix columns
      Rows    []waveMatrixRow   // ordered: wave 1, 2, 3..., wave 0 last
      Sections []waveSection     // existing flat-list view, for the fallback
      ZeroPhases bool             // true when len(Phases)==0; flip to flat-list-only render
  }
  type waveMatrixRow struct {
      Wave  int
      Cells []waveMatrixCell // one per phase, in Phases order; LAST entry is the "Unphased" column
  }
  type waveMatrixCell struct {
      PhaseSlug string  // empty for the Unphased column
      Count     int
      Dots      ColumnDistribution // {Todo, InProgress, Testing, Done} counts
      Tickets   []*domain.Ticket   // for hover/expand if you want; OK to omit if not used
  }
  ```
- Build matrix by walking `ListTickets` once and incrementing `[wave][phaseID-or-nil]`. The "Unphased" column accumulates tickets with `PhaseID == nil`.
- Phase ordering: by `Number` ascending (matches `/phases` ordering).
- Wave ordering: ascending; wave 0 last (existing convention).
- Hard rule: assert `sum(cells.count) == len(tickets)`. If they diverge (orphan PhaseID pointing at a deleted phase, etc.), log a warning AND surface a debug banner on the page. Don't silently drop.

### Template (`internal/web/templates/pages/phases/waves.tmpl`)

- Top section: `<table class="wave-matrix">` rendered when `len(Phases) > 0`.
  - Header row: blank corner cell + one `<th>` per phase (link to phase detail) + final `<th>` "Unphased" (muted styling).
  - Body rows: row header = `<th>Wave N</th>` (or `Wave 0 (unassigned)` muted).
  - Each cell:
    - Empty: render `<td class="wave-cell wave-cell-empty">—</td>` (muted).
    - Non-empty: `<td class="wave-cell">` containing a link wrapping `{count} <span class="dots">{distribution}</span>`. Link goes to `/p/{slug}/phases/{phaseSlug}` (no `?wave=` filter for now — phase detail does not consume it).
- Below the matrix: `<details class="wave-expanded">` collapsed by default with `<summary>Show ticket lists per wave</summary>`. Inside, the existing per-wave card layout (the current flat-list — keep verbatim, just nest under the details).
- When `len(Phases) == 0`: skip the matrix entirely, render the existing flat-list at the top level (the matrix degenerates to one column which is worse than the original).
- Empty project (no tickets at all): existing empty-state still wins.

### CSS (`internal/web/static/_src/input.css`)

- `.wave-matrix` — `<table>` with `border-collapse: separate`, `border-spacing: 4px` for breathing room (or use CSS Grid; whichever feels right).
- `.wave-matrix th`, `.wave-matrix td` — padded cells, sticky-positioned column header would be nice but ship without if it complicates layout.
- `.wave-cell` — accent-tinted background, count and dots stacked vertically.
- `.wave-cell-empty` — muted background, just the em-dash.
- `.wave-cell-dots` — flex row of small `.dot-{column}` circles (reuse existing safelist).
- `.wave-expanded` — wrapper for the fallback details element; visually distinct from the matrix.
- Add `wave-matrix`, `wave-cell`, `wave-cell-empty`, `wave-cell-dots`, `wave-expanded` to tailwind safelist.

### Tests (`internal/web/handlers_phases_test.go`)

- `TestWaves_MatrixCellsBucketCorrectly` — fixture with 3 phases, 6 tickets across 3 waves. Assert that the rendered matrix has the expected counts in the right `(wave, phase)` cells. Use string-search on the rendered HTML or assert on the page-data struct directly.
- `TestWaves_UnphasedColumnIncludesPhaseLessTickets` — fixture with one phase-less ticket. Assert the Unphased column shows count=1 in the right wave row.
- `TestWaves_NoMatrixWhenZeroPhases` — fixture with zero phases. Assert no `<table class="wave-matrix">` in output; flat-list still renders.
- `TestWaves_NoTicketsLost` — fixture with N tickets total. Assert `sum(matrix.cells.count) == N` after handler runs.

## Verification

- `go test ./internal/web/...` green.
- Manual: `/p/tickets-please/waves` shows a matrix; cells link into phases; expanded view still available below.
- `/p/liquidity-hud/waves` (large fixture) — matrix renders; many phases means horizontal scroll on narrow viewports (acceptable for v1).
- Project with one phase + many waves — matrix is a thin column; still useful.
- Project with zero phases — matrix is omitted, flat-list takes over; no degraded UX.

## Hard rules

- Sum of cell counts MUST equal `len(tickets)` returned by `ListTickets`. Surface mismatch on the page (don't crash, don't silently drop).
- "Unphased" is a real column, not an aside. Tickets with `PhaseID == nil` are first-class citizens.
- Wave 0 is "unassigned" and gets its own row sorted last. Same convention as the existing service layer.

## Out of scope

- `?wave=N` filter on phase detail. Cells link to plain `/phases/{phaseSlug}`; future ticket can plumb the filter through.
- Drag-and-drop ticket-between-waves. Buttons-only, same as the rest of the UI.
- Wave creation/edit affordance — waves are integers on tickets; they "exist" by being referenced.
- Cross-project wave aggregation (single-project view).

## Critical files

- `/home/dan/code/tickets_please/internal/web/handlers_phases.go` — `handleWaves`.
- `/home/dan/code/tickets_please/internal/web/templates/pages/phases/waves.tmpl` — full rewrite, but the existing flat-list nests under the new `<details>` so most of its markup is reused.
- `/home/dan/code/tickets_please/internal/web/static/_src/input.css` + `tailwind.config.js` — new matrix classes.
- `/home/dan/code/tickets_please/internal/svc/waves.go` — DON'T touch ListWaves; keep using ListTickets + in-handler bucketing per past learning.

## Notes

- Past learning (e749b038): `ListWaves(slug, nil)` returns wave summaries for **phase-less tickets only**. The cross-cutting handler must keep using `ListTickets` directly. This ticket honors that rule.
- Past learning (5bb597ce): integer-divide percentages floor and stay ≤100, which is what we want for visual segments. Same applies if we want a percent-of-row visualization — not required for v1.
- Past learning (89efc4cf): native `<details>` for the fallback expanded view. No JS needed.
- The matrix is plain HTML `<table>` — easier and more accessible than CSS Grid for tabular data with row + column headers.
- Parallelizable with the phases-index ticket; both touch handlers_phases.go but in different functions, easy merge.
