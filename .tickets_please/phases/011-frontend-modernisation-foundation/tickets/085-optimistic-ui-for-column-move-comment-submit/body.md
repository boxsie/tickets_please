Make the app feel "appy" — actions land instantly client-side, server confirms via SSE shortly after.

## Acceptance

- Column-move modal submit: dialog closes immediately, status badge updates client-side, toast "Moving…" appears; on server-confirmed SSE event, toast becomes "Moved by you"; on server error (HX-Trigger error event), the optimistic change reverts and an error toast shows.
- Comment submit: textarea clears immediately, optimistic comment row appears with subtle "sending…" affordance; on SSE event echoing the new comment, the optimistic row is replaced with the canonical one (matched by client-id); on error, optimistic row turns red with a "retry" affordance.
- Idempotency: client-generated `Idempotency-Key` header on POSTs so the eventual SSE echo can match-and-replace without race.
- Tests cover: happy path; server-rejection path reverts optimistic state.
- No optimism for completion or delete — those require server validation up front.

## Hints

- Datastar signals are the natural fit for the optimistic state (`$optimisticColumn = "in_progress"`).
- The "Sending…" affordance can be pure CSS (opacity + spinner). Replace on SSE match by comment-row id.
