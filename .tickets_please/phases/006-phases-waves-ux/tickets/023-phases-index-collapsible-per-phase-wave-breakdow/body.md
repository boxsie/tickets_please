## Goal

Turn `/p/{slug}/phases` from a flat table of phase rows into a drill-down view: each phase row collapses by default and expands (via HTML `<details>`) to reveal that phase's waves with ticket lists. Default-collapsed keeps the index compact; opening one row gives full per-phase detail without leaving the page.

## Scope

### Handler (`internal/web/handlers_phases.go`)

- `handlePhasesIndex` (~line 38) currently calls `ListPhases(slug)` and shoves the slice straight into the page data. Enrich it: for each phase, compute `[]waveSection` = waves containing tickets in this phase × wave.
- Reuse the existing in-handler bucketing pattern from `handleWaves` (lines 347-403) — call `ListTickets(slug)` once for the whole project, then for each phase walk the result and bucket tickets by `(phase_id, wave)`. Wave 0 (unassigned) sorts last per the existing convention.
- New page-data shape:
  ```go
  type phasesIndexData struct {
      Project *domain.Project
      Phases  []phaseWithWaves
  }
  type phaseWithWaves struct {
      Phase *domain.Phase
      Waves []waveSection // reuse the existing waveSection type
  }
  ```
  Reuse the existing `waveSection` from the waves handler — declare it where appropriate so both consume it.
- Phase-less tickets are NOT included in this page (this is a phases index; phase-less tickets show up on the waves page as the "Unphased" column per ticket 2).

### Template (`internal/web/templates/pages/phases/index.tmpl`)

- Replace the flat `<table>` with one `<details class="phase-row">` per phase.
- `<summary>` content: phase name (link to phase detail), counts ("X active / Y total"), summary preview if non-empty.
- Expanded body: per-wave subsection. For each wave, render heading ("Wave 0 (unassigned)" or "Wave N") + a `<ul class="ticket-list">` matching the dashboard's pattern (`.dot-{column}` status dot + ticket title link).
- Default `<details>` state: collapsed (no `open` attribute). Optional polish: open the first phase by default if you think that's nicer; default to closed if uncertain.
- "+ New phase" CTA stays at the top, as today.

### CSS (`internal/web/static/_src/input.css`)

- New class `.phase-row` for the outer details element — visual match with the existing card pattern.
- `.phase-row > summary` gets the click affordance (cursor: pointer; reset list-style-none).
- `.phase-row[open] > summary` can have a subtle accent border-left to indicate active.
- Reuse `.ticket-list` and `.dot-*` from the dashboard — already safelisted.
- Add `phase-row` to tailwind safelist.

### Tests (`internal/web/handlers_phases_test.go`)

- New: `TestPhasesIndex_EnrichesWithWaves` — fixture project with 2 phases, 4 tickets distributed across two waves and both phases. Assert that the page-data per-phase wave breakdown matches expectations (wave numbers in the right order, tickets in the right buckets, no leakage between phases).
- New: `TestPhasesIndex_PhaseLessTicketsExcluded` — fixture with one phase-less ticket; assert it doesn't appear in any phase's wave breakdown.
- Existing: `TestPhasesIndex_*` — update assertions if HTML structure changed (table → details).

## Verification

- `go test ./internal/web/...` green.
- Manual: visit `/p/tickets-please/phases`. All phase rows render collapsed; clicking a row reveals waves with ticket lists.
- Visit `/p/liquidity-hud/phases` (large fixture). Page renders fast; expanding a phase with many tickets stays responsive.
- Visit a project with one phase + zero tickets: phase row renders, expands to "no tickets in this phase yet" hint.

## Hard rules

- Do NOT silently drop tickets. If `ListTickets` returns a ticket with a `PhaseID` pointing at a non-existent phase, log a warning; the user should see the ticket *somewhere* (waves page handles this case via "Unphased").
- Do NOT lose the existing "Delete phase" affordance — phase detail still owns it; the index just becomes a richer entry point.

## Out of scope

- Per-wave filter on phase detail (`?wave=N` query param). Future ticket — the matrix view links anchor on phases, not phase+wave.
- Animated expand/collapse — `<details>` native is fine; no JS needed.
- Drag-to-reorder phases.

## Critical files

- `/home/dan/code/tickets_please/internal/web/handlers_phases.go` — `handlePhasesIndex`, `handleWaves` (for the bucketing pattern to reuse).
- `/home/dan/code/tickets_please/internal/web/templates/pages/phases/index.tmpl` — full rewrite.
- `/home/dan/code/tickets_please/internal/web/static/_src/input.css` + `tailwind.config.js` — new `.phase-row` class.

## Notes

- Past learning (e749b038): `ListWaves(slug, nil)` returns wave summaries for **phase-less tickets only** — for cross-cutting work, always use `ListTickets` + in-handler buckets. This ticket follows that pattern.
- Past learning (89efc4cf): native `<details>` is the right primitive when there's no JS framework — open/close, accessibility, click-outside all "just work."
- Past learning (c9d8e384): if you add a new partial, the loader needs to ParseFS `partials/*.tmpl` as a glob — single-file ParseFS silently breaks cross-partial template invocations.
