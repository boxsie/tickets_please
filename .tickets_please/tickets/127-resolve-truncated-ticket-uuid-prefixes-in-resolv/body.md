## Problem

Agents keep failing `get_ticket`/etc. with errors like *"the B3 ticket id from memory (31ca06c1) doesn't resolve"*. `31ca06c1` is a **truncated 8-char UUID prefix** — exactly what the web UI renders (`internal/web/components/pages/tickets/props.go:127` `shortID` → `id[:8]`, also `users/data.go:52`). An agent captures that stub from a card/log into memory, hands it back as `ticket_id`, and nothing accepts it:

- `parseTicketShortcode` sees no `/` and non-digits → classifies it "opaque" → passthrough unchanged.
- downstream `get_ticket` does an exact-UUID match → not found.

So the one ID format agents most often capture from the UI is the one format nothing accepts. The `<slug>/<number>` shortcode feature (ticket 534adaa9) doesn't help here because the agent never had the *number*, only the truncated UUID.

## Fix

Extend `svc.ResolveTicketRef` to resolve a **unique UUID prefix** as a backstop, reusing the existing `WalkTickets` pass:

- In the `!isShortcode` branch (currently pure passthrough), attempt prefix resolution before falling back to passthrough.
- A candidate is a pure-hex string (`[0-9a-fA-F]`, with at least one hex *letter* so pure-digit refs stay number-shortcodes), length 4..31 — so full dashed UUIDs (36 chars, contain `-`) and arbitrary opaque ids still pass through untouched.
- Support both bare `31ca06c1` (scoped to the session-bound `defaultSlug`) and `<slug>/31ca06c1` (scoped to the named project).
- Walk the resolved project's store; `strings.HasPrefix` (lowercased) on `tr.ID`.
  - exactly 1 match → resolve to that UUID
  - 0 matches → fall back to passthrough (preserve today's behaviour; downstream still errors)
  - >1 matches → `ErrInvalidArgument` naming the prefix + project + candidate count (ambiguous)
- If the project store can't be resolved (unknown/unbound) → fall back to passthrough, don't hard-error.

## Acceptance

- `ResolveTicketRef(ctx, slug, id[:8])` returns the full UUID when unique.
- Full dashed UUIDs and `not-a-shortcode-xyz` still pass through unchanged (existing `resolve_ticketref_test.go` cases stay green).
- Bare numbers / `<slug>/<number>` still resolve via the number path (priority over prefix).
- Ambiguous prefix → ErrInvalidArgument; tests for unique + ambiguous + passthrough.
- All ticket-targeting tools inherit it for free (they all go through `resolveTicketID`/`resolveTicketArg`).
