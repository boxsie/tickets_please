## Goal

After the per-phase wave breakdown shipped, the standalone `/waves` matrix page is redundant — the same information surfaces (better) inside each phase row. Remove it. Then polish `/phases` so the expanded view looks like the rest of the app instead of a flat dump of headings + bullet lists.

## Scope

### 1. Delete /waves

- Remove the route from the router (GET /p/{slug}/waves).
- Delete `handleWaves`, `wavesPageData`, `waveSection`, `waveMatrixRow`, `waveMatrixCell`, `columnDistribution`, `buildWaveMatrix`, `bucketTicketsByWave` from `internal/web/handlers_phases.go`. Replace `bucketTicketsByWave` callers (only `bucketTicketsByPhaseAndWave`) by inlining the wave-bucketing logic OR keeping a private helper next to it — whatever stays clean.
- Delete `internal/web/templates/pages/phases/waves.tmpl`.
- Delete the "Waves" link from the sidebar's per-project nav (`partials/sidebar.tmpl`).
- Delete the "View waves" button from the phases-index header (`pages/phases/index.tmpl`).
- Delete waves CSS in `internal/web/static/_src/input.css`: `.wave-matrix`, `.wave-cell*`, `.wave-matrix-*`, `.wave-debug-banner`, `.wave-expanded*`. Keep `.wave-section` if it's referenced elsewhere; otherwise drop it too.
- Drop the corresponding safelist entries from `tailwind.config.js`.
- Delete waves tests: `TestWaves_*`, `TestBuildWaveMatrix_*`. Keep the phases-index tests intact.
- Drop any waves-related Playwright walkthrough steps.
- Rebuild Tailwind, rebuild binary, restart service.

### 2. Polish /phases

The collapsed view already looks decent. The expanded body is what feels "dumped" — no separation between waves, ticket rows are 2-line and lack visual rhythm, no sense of completeness at a glance. Direction:

- **Mini status distribution on the phase summary**: a small horizontal bar (like the dashboard's `.status-bar`) segment-tinted by todo/in_progress/testing/done counts, rendered at the right edge of the collapsed summary so users can read project shape at a glance without expanding. Replace or augment the bare "X active / Y total" text.
- **Wave headings inside the expanded body**: add a chip-style wave number ("W1" in a small rounded pill) plus the count. Subtle horizontal divider between waves so they read as separate groupings.
- **Ticket rows**: single-line layout (chevron → title → spacer → small column-coloured badge). Drop the current 2-line stack. Hover state matches `.ticket-list li:hover`.
- **Empty phase state inside expanded body**: keep the existing hint, but center it and give it a subtle muted background card rather than a bare paragraph.
- **Spacing**: add breathing room between waves (gap rather than margin so it works inside a flex container). Phase row summary gets a slightly tinted background when expanded so the body visually attaches to its header.
- **Tickets list in a phase wave**: drop the description below the title for now; the title + badge is enough at the index density.

### Templates / files

- `internal/web/templates/pages/phases/index.tmpl` — most of the work lives here.
- `internal/web/static/_src/input.css` — extend the `.phase-row` family with sub-classes for the status bar, wave chip, ticket row variant.
- `internal/web/static/_src/tailwind.config.js` — safelist any new pattern-matched classes.

### Optional polish (only if cheap)

- Tabular alignment on the collapsed-row counts column (use a fixed-width container so all phases line up vertically when scanning a long list).
- "+ New ticket" link inline within an empty-phase expanded body, keyed off the project slug.

## Verification

- `go test ./...` and `npx playwright test` both green.
- `/p/tickets-please/phases` and `/p/liquidity-hud/phases` look professional, with clear separation between waves inside an expanded phase, status distribution visible without expanding, ticket rows compact and consistent with `.ticket-list` elsewhere.
- `/p/{slug}/waves` returns 404 (route gone). Sidebar no longer shows a "Waves" link. Phases index header no longer shows a "View waves" button.
- Search results, dashboard, board, summary, ticket detail — all unaffected.

## Hard rules

- Don't keep dead code. If a helper / type / class is only there to support /waves, delete it. Tailwind purge will do the right thing once safelist + templates are clean.
- Don't change phase-detail (`/phases/{slug}`) — that page already has its own layout.
- Don't reintroduce the per-wave filter idea on phase detail; phases-as-units is the new model.

## Critical files

- `/home/dan/code/tickets_please/internal/web/router.go` — drop the waves route (search "waves").
- `/home/dan/code/tickets_please/internal/web/handlers_phases.go` — handleWaves + matrix-related types/helpers.
- `/home/dan/code/tickets_please/internal/web/templates/partials/sidebar.tmpl` — drop the "Waves" nav link.
- `/home/dan/code/tickets_please/internal/web/templates/pages/phases/index.tmpl` — polish target + drop "View waves" button.
- `/home/dan/code/tickets_please/internal/web/templates/pages/phases/waves.tmpl` — delete.
- `/home/dan/code/tickets_please/internal/web/handlers_phases_test.go` — drop wave-only tests.
- `/home/dan/code/tickets_please/internal/web/static/_src/{input.css,tailwind.config.js}` — strip waves CSS, add phase-row polish classes.
- `/home/dan/code/tickets_please/e2e/tests/walkthrough.spec.ts` — drop waves screenshot if present.

## Out of scope

- Rebuilding the phase-detail page (different page, separate ticket if/when it needs love).
- Drag-and-drop ticket reordering inside waves.
- A new wave-creation form (waves are still implicit integers on tickets).
