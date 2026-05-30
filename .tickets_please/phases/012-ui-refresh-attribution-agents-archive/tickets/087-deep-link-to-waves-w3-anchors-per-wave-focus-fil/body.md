User lives on phases→waves. Make wave navigation first-class: shareable links to a specific wave, collapse state that survives reload, optional single-wave focus.

## Acceptance

- Each wave section in phase-detail gets `id="w{number}"` (or `id="w-unassigned"` for the 0 wave). Anchor links work; landing on `…/phases/{phase}#w3` scrolls + highlights the section.
- Phases-index: per-phase `<details>` open/closed state persisted in `localStorage` keyed by `phase_id` so reload remembers which phases were expanded.
- New `?wave=N` query param on phase-detail: when present, only that wave is rendered (full-page focus mode), with a "View all waves" link to clear the filter. Server-side filter, not client hide.
- "Focus on this wave →" link added to each wave header in phase-detail → links to `?wave=N`.
- Same `?wave=N` filter also wired on phase-index (filters every phase's expanded body to just that wave — handy for cross-phase wave alignment).
- Tests cover the wave filter on phase-detail handler.

## Hints

- The collapse-state localStorage key should be scoped to the project so two projects with phases of the same slug don't collide: `tp:phase-open:{project_id}:{phase_id}`.
- Smooth-scroll the hash navigation (`scroll-behavior: smooth` on `html`).
