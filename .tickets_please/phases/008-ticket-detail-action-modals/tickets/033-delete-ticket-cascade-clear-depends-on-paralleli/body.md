## Goal

Today `Service.DeleteTicket` refuses if any other ticket lists the doomed one in `depends_on`. Real-world this just blocks legitimate deletes — the user has to hand-edit yaml or update each dependent. Switch to cascade-clean: drop the doomed id from every other ticket's `depends_on` / `parallelizable_with` slices, then delete the ticket. Single StageOp so the auto-commit captures both writes atomically.

## Scope

### svc

- `Service.DeleteTicket` (`internal/svc/tickets.go`):
  - Replace the dependents-refusal block with a walk that builds a list of `{otherTicket, relDir, absDir}` for every other ticket whose `DependsOn` or `ParallelizableWith` contains the doomed id.
  - For each, re-read its `ticket.yaml` (UpdateTicket pattern — preserves fields the cache doesn't model), filter out the doomed id from both slices, bump `UpdatedAt`, stage a yaml rewrite via the same StageOp.
  - After commit success: mutate the cached `*domain.Ticket` slices in-place under `lp.Lock`, recompute `BlockedBy` for each.
  - Keep the `done`-refusal as-is — completion stays sacred.

### MCP tool description

- Drop the "refuses on dependents" half of the description. New text says it auto-clears any `depends_on` / `parallelizable_with` references in other tickets and updates them.

### Web

- Detail-page Delete dialog: replace the "tickets that depend on this will refuse" hint with a softer note that any references will be auto-cleared.

### Docs

- SPEC.md MCP-server table row for `delete_ticket`, and the `DeleteTicket` line in the Service API list — both now describe cascade rather than refusal.

### Tests

- Flip `TestDeleteTicket_RefusesDependents` → `TestDeleteTicket_CascadesDependentRefs`: assert the dependent ticket's `DependsOn` no longer contains the doomed id (both via `GetTicket` and by reading the on-disk yaml), and that the doomed ticket is gone.
- Add a parallel-with case so the second slice is exercised.

## Out of scope

- Cascade-deleting the dependents themselves (we only clear the ref, not the ticket).
- Cross-project cascade (DeleteTicket already scopes to the host project; cross-project deps are rejected at create time).
- Soft-delete / undo.

## Hard rules

- Still refuses on `done` (preserves no-reopen).
- All writes (cascade rewrites + the RemovePath) live in one StageOp under the per-project flock — partial state can never be observed.
