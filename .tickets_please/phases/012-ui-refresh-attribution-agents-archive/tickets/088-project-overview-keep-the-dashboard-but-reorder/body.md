User confirmed: "overview is good, I like it". Don't kill it. Just reorder so phases/waves is the lead surface and the dashboard pieces sit below.

## Acceptance

- Project-detail page order becomes:
  1. Header (title, slug, description, page actions — "Browse phases" replaces "Open board" per [[kill-the-board-redirect]]).
  2. Phases-with-waves block (reuse the same `<details>`-collapsible component from phases-index). Default expanded for any phase with open tickets.
  3. Metric grid (Total / Active / In progress / Done).
  4. Status distribution bar + legend.
  5. Two-column dashboard grid: Ready / Recent activity.
  6. Recent learnings.
  7. Danger zone (delete project) collapsed.
- The Phases card that's currently a tiny table at the bottom is gone — its content is now the lead phases block.
- "+ New phase" inline action button on the lead phases block (so creating phases doesn't require navigating away).

## Hints

- Reuse the `WaveSection` component from [[migrate-phases-to-templ]] so the lead block matches phase-index exactly.
- The metrics grid should compact to one row on wide screens since it's secondary now.
