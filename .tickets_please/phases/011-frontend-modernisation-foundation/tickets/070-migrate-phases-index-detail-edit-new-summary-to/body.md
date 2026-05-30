Port the phases surface — the user's most-used spine view. Especially important the rendered output is visually identical, because Phase 2 will then reshape it heavily.

## Acceptance

- Ported to `internal/web/components/pages/phases/`: `index.tmpl`, `detail.tmpl`, `edit.tmpl`, `new.tmpl`, `summary.tmpl`.
- Ported partials: `phase_summary_view.tmpl`, `phase_summary_edit.tmpl`, `assign_phase_form.tmpl`.
- The `&lt;details&gt;`/`&lt;summary&gt;` collapsible row from `index.tmpl` preserved (this is what the user actually uses to expand waves).
- The phase-row progress bar (todo/in_progress/testing/done segments) preserved — Tailwind-native, not inline styles where possible (one inline `width: X%` is OK).
- Wave grouping render preserved exactly; both phase-detail and phase-index render waves consistently (extract `WaveSection(props)` component to dedupe).
- Smoke tests pass.

## Hints

- `internal/web/templates/pages/phases/index.tmpl` is currently 50 lines of dense template — extract the wave section to a shared component so phase-detail and phase-index use the same renderer.
- Keep the `&lt;dot dot-{column}&gt;` semantic — Phase 1 W3 may animate it on column changes.
