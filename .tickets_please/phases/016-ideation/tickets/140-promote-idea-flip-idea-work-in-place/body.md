The payoff of the kind-axis design: an idea that matures becomes a real ticket with one field flip, keeping its comments + embedding history (no cross-store copy).

## Changes
- **`internal/svc/ideation.go`** (new) — `PromoteIdea(ctx, ticketID, comment, optionalPhase)`: read record, assert `Kind == KindIdea` (else error), flip to `KindWork`, write a `system_promote` audit comment, enqueue re-embed, update cache, optionally assign to a phase. Model it directly on `flipArchive` in `internal/svc/archive.go` (same read→flip→comment→enqueue→cache shape).
- **`internal/domain/types.go`** — add `CommentKindSystemPromote CommentKind = "system_promote"` next to the archive kinds (types.go:26-32).
- **eventbus** — add a `KindTicketPromoted` event mirroring `KindTicketArchived/Unarchived` (#adcc2e82) so the web layer can live-patch later.
- **`internal/mcptools/tools.go`** — register `promote_idea` (args: `ticket_id` required, `comment` required — audit-trail rule, `phase_id_or_slug` optional). Update the **three-place lockstep tool count** + canonical-list test (learning #a28797b8).

## Notes
- Promotion keeps the `todo` column (ideas already sit there); it only changes `kind`. The promoted ticket immediately appears in default `list_tickets`.
- Comment is required, like every state-changing op.
- Dogfooding gotcha (#adcc2e82): promoting a real ticket auto-commits under the per-project flock — use a throwaway idea for live verification.

## Acceptance
- `promote_idea` on an idea: kind→work, `system_promote` comment present, ticket now visible in default `list_tickets`, original comments intact.
- `promote_idea` on a `work` ticket errors.
- Unit test for the flip + comment + (optional) phase assignment.
