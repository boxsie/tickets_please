## Ideation — first-class spitball ideas

### Problem
Today, to capture half-formed ideas we **hijack real tickets**: make a phase, fill it with tickets that are actually just ideas. That fights the system's grain — a ticket demands a lifecycle (`todo → in_progress → testing → done`), nags for `learnings` to complete, and shows up in `list_tickets` / `ready_only` / `depends_on`. An idea has none of that. Every ideation-ticket is a little lie you have to remember not to act on.

### Goal
A dedicated home for spitballs: cheap to create (title + brain-dump), **hidden from normal work surfaces by default**, fully **searchable behind an opt-in filter** (exactly like `archived`), and **promotable in place** into a real ticket when an idea matures — keeping its discussion + embedding history.

### Approach (decided)
Ideas are a **`kind` axis on tickets** (`work` default, `idea`), orthogonal to `column` and `archived` — **not** a parallel entity. This reuses the embedding/search stack, comments, and storage for free, and makes promotion a one-field flip instead of a cross-store copy. `kind` mirrors the proven `archived` mechanism beat-for-beat: default-hidden + `include_*` opt-in + post-filter in `svc`. Empty `kind` == `work`, so the change is **backfill-free** — every existing `ticket.yaml` round-trips unchanged. Ideas stay in the `todo` column but are hidden by the `kind` filter; promotion flips `kind: idea → work` (column unchanged).

**Scope:** per-project (a ticket axis), not a global cross-project scratchpad — that's a later concern once we build more global presence.

### Key reference learnings (already in this corpus)
- **#7e260496** (include-archived toggle): all four input structs already carry `IncludeArchived`; svc post-filters. Web toggle is a **link, not a checkbox** (`internal/web/archived_pref.go`, `resolveShowArchived` cookie precedence, threaded through page props). Mirror this for `include_ideas`.
- **#adcc2e82** (archive UI): `attribution.ArchivedPill` no-op-when-false pattern, eventbus `KindTicketArchived/Unarchived`, `system_archive/unarchive` comment kinds, delegated `dialogs.js`, SSE PageActions morph, modals-always-present. Dogfooding gotcha: archiving a real ticket leaves git commits under the per-project flock — use a throwaway ticket for live tests.
- **#a28797b8**: new MCP tools must update the **three-place lockstep tool-count** convention; a canonical-list test catches drift loud.

### Definition of done
Create an idea, confirm it's hidden from default `list_tickets`/search, surfaced via `include_ideas` and the dedicated `list_ideas`/`search_ideas`, blocked from `complete_ticket`, and `promote_idea` turns it into a real ticket in place. Web board shows an ideation lane separate from work columns. Docs updated.
