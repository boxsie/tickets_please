Make ideas invisible on the normal work surfaces unless explicitly asked for — a direct mirror of the `IncludeArchived` mechanism (see learning #7e260496: all four input structs already carry `IncludeArchived` and the svc post-filters).

## Changes
- **`internal/domain/inputs.go`** — add `IncludeIdeas bool` to `ListTicketsInput` (:55), `SearchTicketsInput` (:65), `SearchLearningsInput` (:127), `SearchCommentsInput` (:78). Default false = ideas dropped. Document like the existing `IncludeArchived` comments.
- **`internal/svc/tickets.go`** — in `ListTickets` add a post-filter next to the archived one (~457): `if t.Kind == domain.KindIdea && !in.IncludeIdeas { continue }`.
- **`internal/svc/search.go`** — same post-filter beside each archived check: SearchTickets (~215), hydrateCommentHit (~426), hydrateLearningHit (~569).
- **`internal/mcptools/tools.go`** — add an `include_ideas` boolean param to the `list_tickets` / `search_tickets` / `search_learnings` / `search_comments` registrations, and wire it into each handler's input struct (mirror how `include_archived` is read).

## Notes
- `kind` and `archived` are independent axes: an archived idea needs BOTH `include_archived` AND `include_ideas` to surface. Keep the two filters as separate `&&` clauses, don't merge.
- `get_ticket` stays unfiltered (direct lookup always works), same as archived.

## Acceptance
- Create an idea ticket → it's absent from default `list_tickets` and `search_tickets`, present when `include_ideas=true`.
- A `work` ticket is unaffected by the flag.
- Unit tests mirroring the archived post-filter tests (deterministic via `ListTickets`, not the async embed path — see #7e260496 timing gotcha).
