## Testing evidence
Flipped TestDeleteTicket_RefusesDependents → TestDeleteTicket_CascadesDependentRefs covering both depends_on and parallelizable_with cascade; verifies the doomed ticket is gone, the cached DependsOn / BlockedBy / ParallelizableWith on the dependent tickets no longer reference the doomed id, and the on-disk yaml for the dependent matches (cascade was persisted, not just cached). All four DeleteTicket tests pass:

- `go test ./internal/svc/ -run TestDeleteTicket -count=1 -v` → PASS x4 (HappyPath, RefusesDoneTickets, CascadesDependentRefs, RequiresSession)
- `go test ./internal/svc/ -count=1` → ok (no regressions)
- `go test ./... -count=1` → all green across cache/embed/mcptools/store/svc/vecindex/web/worker

End-to-end verification on live data:
1. Built (make build) and restarted the user's tickets-please.service unit.
2. Re-registered against /home/dan/code/jobsworth.
3. Called delete_ticket on cf39303d-92f6-4e4f-8f3b-aee9e28386c4 (the user's stuck Bootstrap ticket) — succeeded with `{"deleted_ticket": "..."}`. Pre-delete it had 3 dependents (117fcd30 done, c8352345 done, 6961f92f in_progress). Post-delete, get_ticket on 117fcd30 confirms `depends_on: []` — cascade landed.

## Work summary
Replaced the dependents-refusal with a cascade rewrite in `Service.DeleteTicket` (internal/svc/tickets.go). Walk every other ticket in the project, compute whether it carries the doomed id in `DependsOn` or `ParallelizableWith`, and for each match: locate its dir via `findTicketDir`, re-read the on-disk `ticket.yaml` (UpdateTicket pattern — preserves fields the cache doesn't model), filter out the doomed id from both slices, bump UpdatedAt, stage a yaml rewrite. All cascade rewrites + the `RemovePath` go into a single StageOp, so the auto-commit captures the entire fan-out atomically under the per-project flock — partial state can never be observed. Commit caption now reads `delete ticket <slug>/<id> (cleared N dependent ref(s))` when N > 0.

Post-commit cleanup mutates the cached `*domain.Ticket.DependsOn` / `ParallelizableWith` slices in-place so the next read sees consistent state without waiting for fsnotify; `BlockedBy` is recomputed by the existing GetTicket/ListTickets path off the freshly-mutated `DependsOn`. The done-refusal stays — completion remains sacred.

Added two tiny helpers near `computeBlockedBy`: `containsID([]string, string) bool` and `removeID([]string, string) []string` (returns nil for empty result so the on-disk yaml round-trips through `omitempty`).

Updated text everywhere the old "refuses on dependents" language lived: `delete_ticket` MCP tool description (now says it auto-clears refs), the web detail-page Delete dialog's hint paragraph, the SPEC MCP tool table row, and the SPEC Service API list entry for `DeleteTicket`. README's only mention of `delete_ticket` is the tool-counts row, no behavior text — left as-is.

Test work: flipped `TestDeleteTicket_RefusesDependents` → `TestDeleteTicket_CascadesDependentRefs` covering both DependsOn and ParallelizableWith. The new test asserts the doomed ticket is gone via GetTicket, the dependents' cached refs are cleared (DependsOn empty, BlockedBy empty, ParallelizableWith empty), and the on-disk `ticket.yaml` was rewritten (not just cache-mutated). The other three DeleteTicket tests (happy path, refuses-done, requires-session) keep passing unchanged.

Build → restart → re-register against jobsworth → delete_ticket on cf39303d. Worked first try; the previously-stuck ticket is gone, the live in_progress dependent (6961f92f) now has empty `depends_on`, and the two done dependents (117fcd30, c8352345) likewise. The user can now delete tickets with refs without manually rewiring first.

## Learnings
- `removeID` returning nil rather than `[]string{}` matters: it lets the rewritten ticket.yaml round-trip through `omitempty` so a previously-empty list doesn't suddenly persist as `depends_on: []`. Default Go marshal would emit `null`/empty list otherwise; the existing yaml round-trips work because of this. If you ever need to *force* the field to render as an empty list rather than be omitted, that's a yaml struct tag change, not a slice trick.

- Batching cascade Writes with the doomed-ticket RemovePath into a single StageOp is the load-bearing atomicity guarantee. The StageOp's ordered-ops model (Write applies via rename-into-place, RemovePath via os.RemoveAll) means a crash mid-apply can leave partial state — but never inconsistent state visible to a successful return. The auto-commit caption groups them all under one git commit so `git log` shows the cascade in one place.

- Re-reading each dependent's `ticket.yaml` from disk (UpdateTicket pattern) instead of marshalling the cached `*domain.Ticket` is critical. The cache doesn't carry CompletedByAgentID, CompletedAt, TestingEvidence/WorkSummary/Learnings on done tickets in the same shape the on-disk record holds — and even though we refuse to delete done tickets, *dependents* in the done column still need their yaml rewritten. Marshalling the cached domain.Ticket would silently drop those fields. Same trap UpdateTicket already documented.

- The cache mutation step (`other.DependsOn = removeID(...)`) is only safe under `lp.Lock.Lock()` (exclusive). Earlier I had an instinct to use RLock for the dependent-walk + Lock for the apply, but mixing acquires invites deadlock if anything down-stack also tries to lock. Keep one exclusive Lock for the whole DeleteTicket. The walk is O(active tickets) which is trivial at this scale.

- `findTicketDir` is one-call-per-id and does a fresh WalkTickets each time. For the cascade, that's N+1 walks (one for the doomed ticket plus one per dependent). At project scales <500 tickets it's fine; if it ever matters, hoist a single WalkTickets pass into a `ticketID → relDir` map. Premature today.

- Free behaviour: `BlockedBy` doesn't need explicit recomputation in DeleteTicket because the existing GetTicket path always recomputes via `computeBlockedBy(t.DependsOn, lp.Tickets)`, and once we mutate `t.DependsOn` the next read derives the new BlockedBy automatically. Same argument applies to ListTickets. Saves a chunk of bookkeeping.

- The dependent ticket's body markdown mentioned the doomed id as historical narrative; the cascade does NOT touch body text, only the structural slice fields. That's the correct behaviour — body is freeform documentation, dep refs are the structural contract. If a future feature wants to surface "this ticket was once a dep" in narrative, do it via auto-comment, not body editing.

- HTTP session lifecycle observation: each restart of `tickets-please.service` invalidates all in-memory sessions; the in-process `register_agent` cache is wiped, so any agent (including this MCP client) has to re-register. The `auto_refresh` retry only handles svc-layer expiry, not process restart. Worth keeping in mind when scripting a "build + restart + immediately call MCP tool" loop.
