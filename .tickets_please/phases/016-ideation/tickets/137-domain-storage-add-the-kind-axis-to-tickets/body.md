Introduce a `kind` axis on tickets — the foundation everything else builds on. Mirrors how `Archived` sits on the ticket (orthogonal to `Column`), not a new entity.

## Changes
- **`internal/domain/types.go`** — add a `TicketKind` string type next to `Column` (types.go:12-20) with consts `KindWork TicketKind = "work"` and `KindIdea TicketKind = "idea"`. Add `Kind TicketKind` to the `Ticket` struct (~112-148, sibling to `Archived`). Provide a helper so empty normalises to `work` (e.g. `func (k TicketKind) OrWork() TicketKind`).
- **`internal/store/records.go`** — add `Kind domain.TicketKind `yaml:"kind,omitempty"`` to `TicketRecord` (next to `Archived` at :89). `omitempty` + empty==work means **every existing `ticket.yaml` round-trips unchanged — no migration**.
- **`internal/store/tickets.go`** — map `Kind` in the to-domain / from-domain conversions (normalise empty → `work` on read).
- **`internal/domain/inputs.go`** — add optional `Kind TicketKind` to `CreateTicketInput` (defaults to `work` when empty).
- **`internal/svc/tickets.go`** — `CreateTicket` persists `Kind` (default `work`).

## Notes / gotchas
- Do NOT touch the `Column` enum — ideas live in the `todo` column and are hidden by the kind filter (next ticket), exactly like archived tickets keep their column.
- Keep `formatTicket` (mcptools) emitting `kind` so the field is visible in tool output.

## Acceptance
- `make build && make test` green.
- A ticket created with no kind reads back as `work`; an existing fixture `ticket.yaml` (no `kind:` key) loads as `work`.
- Round-trip test: write a `kind: idea` record, read it back, assert `KindIdea`.
