## Goal

`pages/phases/detail.tmpl` still has a bare "Waves" card listing wave summaries as `Wave N — X active / Y total` bullets. Now that the phases-index expanded view shows a polished per-wave breakdown (W{n} chip + ticket-list rows + dashed dividers), the phase-detail page should reuse the same rendering instead of falling back to the bare list.

## Scope

- `handlePhaseDetail` (handlers_phases.go ~line 134): drop the `ListWaves` call. Instead, fetch tickets for this phase via `ListTickets(slug, phaseSlug)` and bucket them with the existing `bucketTicketsByWave` helper. Update `phaseDetailData` to carry `[]waveSection` instead of `[]domain.WaveSummary`.
- `pages/phases/detail.tmpl` — replace the bare bulleted Waves card with the same per-wave block used by the phases-index template (W{n} chip + heading + count + `phase-wave-tickets` ul). Drop the `<h2>Waves</h2>` wrapper card; the page already has the phase header + summary card + danger zone.
- Reuse existing `.phase-wave-*` classes — already safelisted, no new CSS needed.
- Empty state: if the phase has no tickets, show the same centered hint card with `+ New ticket` CTA used by the phases-index empty-phase state.

## Hard rules

- Don't recompute or re-introduce wave-bucketing logic; reuse `bucketTicketsByWave`.
- Don't touch the danger zone or the summary card.

## Verification

- `go test ./internal/web/...` green.
- Visit `/p/tickets-please/phases/web-frontend` — the wave breakdown matches the same phase row's expanded view on `/p/tickets-please/phases`.
- Empty phase: visit a phase with zero tickets — centered hint card with `+ New ticket` CTA.
