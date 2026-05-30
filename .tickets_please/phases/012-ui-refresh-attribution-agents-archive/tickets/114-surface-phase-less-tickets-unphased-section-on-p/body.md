Tickets don't have to belong to a phase, but today the only place phase-less tickets surface is the board (which W1 kills). Without a home for them they become invisible. Fix that before [[kill-the-board-redirect]] lands.

## Acceptance

- New "Unphased" pseudo-phase rendered on the phases-index page when any phase-less tickets exist. Renders with the same `<details>`-collapsible row + wave-section structure as real phases, but visually distinct (e.g. no progress bar, italic "no phase" label, muted chevron).
- Phase-less tickets grouped by `Wave` inside the Unphased section, same as real phases (so wave-0 / "unassigned" still works).
- Project overview's lead phases block (per [[overview-lead-with-phases]]) also includes the Unphased section.
- Counts displayed: "{N} unphased tickets".
- Empty state: don't render the section if zero phase-less tickets exist.
- Phase-detail filter (`?wave=N` from [[deep-link-to-waves]]) also accepts `?phase=unphased&wave=N` for direct linking.
- Tests cover: index renders the Unphased section when phase-less tickets exist; section hidden when not; wave grouping within it.

## Hints

- `Service.ListTickets` already supports `phase: null` filter — use that to fetch phase-less tickets.
- A "Move to phase" action in the Unphased section's ticket rows is a natural follow-up but not required in v1 — users can still reassign via the existing ticket-detail "Reassign phase" modal.
- This is THE dependency for [[kill-the-board-redirect]] — board kill should not land until this ticket is done, otherwise phase-less tickets disappear from the UI.
