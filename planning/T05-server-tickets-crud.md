---
id: T05
title: Ticket CRUD methods
status: TODO
owner: ""
depends_on: [T02, T03, T04, T15]
parallelizable_with: [T06]
wave: 4
files:
  - internal/svc/tickets.go
  - internal/store/tickets.go
  - internal/domain/slug.go
estimate: medium
stretch: false
---

# T05 — TicketService CRUD

## Scope

Implement the **non-rule-bearing** ticket methods on `svc.Service`: `Create`, `Get`, `List`, `Update`. Backed by the filesystem store and the project cache from T04.

**In:** `internal/svc/tickets.go` with the four CRUD methods, `internal/store/tickets.go` write helpers.

**Out:** **No `MoveTicket`. No `CompleteTicket`. No `AssignTicketToPhase`.** Those are T07 and T16. Don't write them, don't stub them — let those tickets add the methods.

## Files

- `internal/svc/tickets.go` (Create/Get/List/Update only)
- Extensions to `internal/store/tickets.go` (Insert + Update helpers)

## Details

### `CreateTicket(in CreateTicketInput)`

1. Reject empty `Title` (after trim).
2. Lazy-load the project via `s.Cache.Get(ctx, slug)`.
3. Take `loaded.Lock.Lock()`.
4. Validate every entry in `DependsOn` and `ParallelizableWith` references an existing ticket id in this project. Cross-project deps rejected with `ErrInvalidArgument`.
5. Validate `Wave >= 0` (negative numbers rejected; `0` = unassigned is fine).
6. Compute the next `number` = `len(loaded.Tickets) + 1` (nominal — guard against gaps later if any are deleted).
7. Compute the directory name: `fmt.Sprintf("%03d-%s", number, slug.Make(title))` (use `gosimple/slug` or a hand-rolled slugifier; lowercase, dash-separated, ASCII).
8. `StageOp` writing `tickets/<dir>/ticket.yaml` (with `depends_on` / `parallelizable_with` / `wave` / `phase_id` fields) and `tickets/<dir>/body.md`.
9. Commit the StageOp. Auto-commit caption: `[tickets_please] create ticket <slug>/<NNN> [<agent>]`.
10. Insert into `loaded.Tickets`.
11. Enqueue `JobTicketBody` (T10 marker).
12. Return the new `Ticket` (with `blocked_by` computed against current ticket states).

`Column` always starts as `ColumnTodo`. `CreatedBy = agent.id` from the context.

### `GetTicket(id)`

`s.Cache.Get` for the ticket's project (resolve via `WalkProjects` if id is unknown — but in practice the caller usually has the slug in scope from MCP). Read from `loaded.Tickets[id]`.

### `ListTickets(in ListTicketsInput)`

- Lazy-load.
- Filter by `Column` if specified (`nil` = no filter).
- Filter by `PhaseIDOrSlug`: `nil` = any; `*string("")` (sentinel) = phase-less only; `*string("foo")` = that phase.
- Filter by `Wave`: `nil` = any wave; `*int(N)` for `N >= 0` = exactly that wave (0 means unassigned).
- If `ReadyOnly` is true, post-filter to tickets where `BlockedBy` is empty AND `Column ∈ {todo, in_progress}`.
- Order: `ORDER BY (Wave, CreatedAt)` — tickets within a wave ordered by creation time; wave 0 (unassigned) sorts first or last depending on caller preference (default: last, so the orchestrator sees structured waves before unstructured tickets).
- Pagination with cursor `<created_at>|<id>` base64'd. Default `Limit=50`, cap `200`.

### Computing `blocked_by`

Read-time only — never stored. For a ticket `t`:

```go
blockedBy := make([]string, 0)
for _, depID := range t.DependsOn {
    dep, ok := loaded.Tickets[depID]
    if !ok || dep.Column != domain.ColumnDone {
        blockedBy = append(blockedBy, depID)
    }
}
```

Computed every time a ticket is converted to its `domain.Ticket` value.

### `UpdateTicket(id, in UpdateTicketInput)`

- Load project; take `loaded.Lock.Lock()`.
- Mutate `loaded.Tickets[id]` (only the supplied fields — `Title`, `Body`, `Wave`).
- `StageOp` rewriting `ticket.yaml` and (if body changed) `body.md`.
- Commit. Caption: `[tickets_please] update ticket <slug>/<NNN> [<agent>]`.
- If title or body changed, enqueue `JobTicketBody`.
- Update `updated_at` to now.
- **Reject** any column-related field (`UpdateTicketInput` doesn't have one per T03; this is a defensive belt-and-braces note for the implementer).
- `Wave` accepts `*int`. `nil` = leave unchanged. `*int(N)` for any `N >= 0` sets the wave (0 = unassigned).

### Slugification

Use a small helper in `internal/domain/slug.go`:

```go
func MakeSlug(s string) string  // lowercase, replace non-alnum with '-', collapse, trim, cap 48 chars
```

If two tickets in the same project somehow produce the same slug (rare with the number prefix), append `-2`, `-3`, etc.

## Acceptance criteria

- [ ] `Service.CreateTicket` lands with `Column = ColumnTodo`, a fresh UUID, and `CreatedBy` populated from the session.
- [ ] The on-disk dir name has the expected `NNN-slugified-title` form.
- [ ] `Service.CreateTicket` with empty title → `domain.ErrInvalidArgument`.
- [ ] `Service.GetTicket` on unknown id → `domain.ErrNotFound`.
- [ ] `ListTickets` with `column = COLUMN_TODO` returns only todo tickets.
- [ ] Pagination: with `limit=2` and 5 tickets, three pages walk the whole set; the third page's `next_cursor` returns empty.
- [ ] `UpdateTicket` with only `title` doesn't blank the body.
- [ ] Auto-commit: each Create/Update produces one git commit attributed to the calling agent.

## Notes

See **Service API > Tickets** and **Data layout** in [`../SPEC.md`](../SPEC.md). T07 will add `MoveTicket`/`CompleteTicket` to the same `tickets.go`; T16 adds `AssignTicketToPhase`. T10 will patch `// T10: enqueue embed job here` markers around your StageOp.Commit calls.
