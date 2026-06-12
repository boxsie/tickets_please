## Goal

Add an **ideation** flag on tickets, modelled exactly on the existing `archived` flag: an idea/brainstorm ticket is hidden from the default work surfaces and you opt into seeing it with an `include_ideation` filter (the mirror of `include_archived`). Ideas live in the same store and use the same columns; they're just filtered out by default so they don't clutter the work queue, board, or search.

## Why this shape

`archived` already solves the "second-class, hidden-by-default, opt-in-to-see" problem and is wired cleanly through the whole stack. Ideation is the same idea with a different intent, so we copy the plumbing rather than invent a new `kind` enum. This also dissolves the earlier open questions: no new lifecycle, no learnings-gate special-case, and **promotion is just "unset the flag"** — an idea graduates into real work the way `unarchive_ticket` brings a ticket back.

The one semantic difference from archived: archived = "this is finished/dead, get it out of the way"; ideation = "this isn't actionable work yet". A ticket could in principle be both (an abandoned idea), so ideation is an independent boolean, not a state in the archived axis.

## Implementation: mirror `archived` at every touch point

**Data model**
- `internal/domain/types.go:146` — add `Ideation bool` next to `Archived` (the doc comment at `:140-146` is the template: "independent of Column… excluded from search_*/list_tickets by default"). Probably no timestamp needed; add `IdeationAt *time.Time` only if we want the audit parity.
- `internal/store/records.go` — add `Ideation` (yaml `ideation,omitempty`) to `TicketRecord`; cache hydration maps it through.

**Filter inputs** (each already carries `IncludeArchived`, add `IncludeIdeation` beside it)
- `internal/domain/inputs.go:55,70,84,133` — `ListTicketsInput`, `SearchTicketsInput`, `SearchCommentsInput`, `SearchLearningsInput`.

**Default-exclude filter points** (each is an `if t.Archived && !in.IncludeArchived` drop — add the `Ideation` twin)
- `internal/svc/tickets.go:457` (list)
- `internal/svc/search.go:215` (tickets), `:426` (comments via `hydrateCommentHit`), `:569` (learnings via `hydrateLearningHit`).
- `ready_only` work-queue filtering should also drop ideation by default.

**MCP tools** (each `include_archived` bool param gets an `include_ideation` sibling; handlers at `tools.go:1003,1585,1626,1672` read it)
- `list_tickets` (`tools.go:228`), `search_tickets` (`:338`), `search_learnings` (`:346`), `search_comments` (`:355`) — add `include_ideation` param + update the tool descriptions (which currently spell out the archived-exclusion rule).
- Setting the flag: simplest is an `ideation` bool on `create_ticket` (and `update_ticket`). For parity with archive we could also add `mark_ideation` / `unmark_ideation` flip tools backed by the same `archiveFlipHandler`-style helper (`tools.go:1314`), writing a `system_*` audit comment — decide whether that ceremony is worth it or whether create/update flag is enough.
- `format.go` — surface `ideation` in `formatTicket` output.

**Web frontend** (mirror however the board/lists currently treat archived)
- New/edit ticket form: an "ideation" checkbox.
- Board / list / phase views: hide ideation by default, with a show-ideation toggle paralleling any existing show-archived control. Ticket card badge for ideation. Regenerate `*_templ.go`.

**Vec index**: no change — index ideation tickets normally (like archived) and filter at hydration, so toggling the flag is free.

## Open questions (smaller now)

1. Do we want the `mark_ideation`/`unmark_ideation` flip tools + audit comments, or is a flag on create/update sufficient?
2. Should there be a per-project default, or is "hidden everywhere by default" fine universally? (archived has no per-project visibility default, so probably fine.)
3. Web: is there already a show-archived toggle to copy, or does archived just never surface in the UI? (drives how much UI work this is.)

## Acceptance

- A ticket can be flagged `ideation` on create (and toggled later), without changing its column.
- Ideation tickets are excluded from `list_tickets`, `ready_only`, `search_tickets`, `search_learnings`, `search_comments`, and the default board — exactly as archived tickets are — and reappear when `include_ideation=true`.
- Un-flagging an ideation ticket makes it a normal work ticket immediately (the promotion path).
- `get_ticket` by id always returns it regardless of the flag (matches archived behaviour).
