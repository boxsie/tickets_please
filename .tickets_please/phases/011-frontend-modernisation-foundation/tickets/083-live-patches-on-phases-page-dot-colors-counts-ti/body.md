The phases page is the user's main view ("I'm always on the phases page looking at the waves open"). Realtime updates here are the highest-value live surface.

## Acceptance

- Phases-index page subscribes to `project:{id}`; phase-detail page subscribes to `phase:{id}` + `project:{id}`.
- On `TicketMoved`: the ticket row's `.dot-{column}` re-renders to the new column; the badge updates; the phase row's progress bar segments rebalance; active/total counts update.
- On `TicketCompleted`: same as moved-to-done, plus the completion timestamp tooltip appears.
- On `TicketArchived`: row visually mutes (or filters out if archived hidden).
- New ticket created in a phase → row inserted in correct wave section.
- Patch granularity: per-row swaps where possible, only fall back to full wave-section re-render when topology changes (new ticket / phase reassign).
- Tests cover: server-side patch-event generation; manual two-tab test plan in completion.

## Hints

- Stable selectors: `#ticket-row-{id}`, `#phase-row-{slug}`, `#wave-section-{phase_id}-{wave}`.
- Reuse the `WaveSection` component from [[migrate-phases-to-templ]] — it can render either the whole section or a single row.
