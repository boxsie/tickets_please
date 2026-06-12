## Goal

Add a first-class **ideation** ticket kind, distinct from normal work tickets. Today brainstorm/idea tickets get filed alongside actionable work and clutter the board, search, and `ready_only` work queues. We want ideas tracked in the same store but cleanly separable from "things to actually build".

## Why

- Ideation tickets are open-ended thought, not a unit of work with acceptance criteria. They don't really flow `todo → in_progress → testing → done`, and they shouldn't show up when an agent asks "what's ready to work on".
- Mixing them in pollutes `list_tickets`, the board, and `search_learnings` (an idea has no "gotchas / how I fixed it" learnings).

## Proposed shape (open for discussion)

Add an orthogonal `kind` field on tickets — default `work`, plus `ideation`. Kind is independent of column/wave/phase. Decisions still to make are listed at the bottom; this ticket can be promoted to its own phase if the surface proves large.

## Integration surface (mapped from the codebase)

There are a lot of touch points — this is why it's worth a dedicated ticket rather than an ad-hoc field:

**Data model & persistence**
- `internal/domain/types.go:107` — add `Kind` to `Ticket` struct (alongside the existing `CommentKind` enum at ~`:24`, which is unrelated).
- `internal/store/records.go:71` — add `Kind` (yaml `kind,omitempty`) to `TicketRecord`.
- `internal/cache/projectcache.go` (~hydrate, ~`:647`) — map record → domain.

**Service layer / inputs**
- `internal/domain/inputs.go` — add `Kind` to `CreateTicketInput` / `UpdateTicketInput`, and a `Kind` filter on `ListTicketsInput`.
- `internal/svc/tickets.go` — `CreateTicket`, `UpdateTicket`, `ListTickets` (apply filter), and crucially `CompleteTicket` (~`:852` the ≥10-char learnings gate — see decisions).
- `internal/svc/validation.go` — optional strict-enum validation for kind. Column flow stays unchanged.

**MCP tools**
- `internal/mcptools/tools.go` — `kind` param on `create_ticket`/`update_ticket`; `kind` filter on `list_tickets` (and maybe `search_tickets`). Likely a default in `list_tickets`/`ready_only` to EXCLUDE ideation.
- `internal/mcptools/format.go:32` — surface `kind` in `formatTicket` output.

**Search / embeddings**
- `internal/vecindex/index.go:15` — existing `Kind` enum (ProjectSummary/TicketBody/Learnings/Comment); decide whether ideation bodies get their own index kind or just a post-filter.
- `internal/svc/search.go` — `SearchTickets` post-filter by kind; `SearchLearnings` should probably skip ideation tickets entirely.

**Web frontend (templ)**
- `internal/web/components/pages/tickets/` — `new.templ` (kind selector), `edit.templ`, `ticket_card.templ` (badge), `metadata.templ`/`detail.templ` (kind row). Regenerate `*_templ.go`.
- `internal/web/handlers_tickets.go:50` — `ticketFormSubmitted` + `handleTicketCreate` extract/pass kind.
- Board / phase / wave list views — decide default visibility of ideation tickets.

## Open design decisions (resolve before building)

1. **Kind values & default** — `work` (default) + `ideation`; room for more later, or keep binary?
2. **Workflow** — do ideation tickets use the same columns, or a simpler lifecycle? If same, does "done" still require `complete_ticket`?
3. **Learnings gate** — relax/skip the ≥10-char `learnings` requirement for ideation completion, or repurpose it as "outcome of the idea"?
4. **Default visibility** — `list_tickets` / `ready_only` / board should default to hiding ideation; opt-in via a `kind` filter. Confirm.
5. **search_learnings** — exclude ideation tickets (recommended) vs include.
6. **Promotion path** — when an idea becomes real work, flip its kind to `work` (and reset column?) vs spawn a linked work ticket. This is probably the most important UX question.

## Acceptance

- A ticket can be created with `kind=ideation` via MCP and the web form.
- Ideation tickets are excluded from default work listings/board and from `search_learnings`, but findable via an explicit kind filter.
- Existing tickets default to `work` with no migration needed (solo project, in-repo store).
