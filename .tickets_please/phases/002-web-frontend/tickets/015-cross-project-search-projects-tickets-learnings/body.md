## Goal

A `/search` page wrapping the four `Search*` Service methods in a single query box with result-type tabs and live (debounced) results via htmx. Top-nav search box submits here.

## Why

The single best feature of tickets_please for power users is "I think I solved this before" — semantic search over completion learnings finds prior wisdom. Humans need that affordance just as much as LLMs. Cross-project search makes the centralised server's resident vector indexes pay off in the UI.

## Scope

### Routes (handlers/search.go)

| Method | Path | Purpose | Service call |
|--------|------|---------|--------------|
| GET | `/search?q=&kind=&slug=&limit=` | Single dispatcher | one of `svc.SearchProjects`, `SearchTickets`, `SearchLearnings`, `SearchComments` |

`kind` is one of `projects`, `tickets`, `learnings`, `comments` (default `learnings` — the most useful one).
`slug` (optional) restricts the search to a single mounted project; empty = cross-project.
`limit` (optional, default 20, max 50).

### Templates

- `internal/web/templates/pages/search.tmpl` — search box (pre-filled from `q`), tab strip (`Projects | Tickets | Learnings | Comments`), result list `#results`.
- Partials per result type:
  - `partials/search_results_projects.tmpl` — project name + slug + matched-text snippet + score.
  - `partials/search_results_tickets.tmpl` — ticket title + project badge + column badge + body excerpt + score.
  - `partials/search_results_learnings.tmpl` — completion learning excerpt + parent ticket title link + project badge + completion date + score.
  - `partials/search_results_comments.tmpl` — comment excerpt + parent ticket link + author + score.
- `partials/search_empty.tmpl` — friendly empty state ("No matches. Try broadening the query or switching tabs.")

### htmx wiring

Search box on the page:
```html
<input type="search" name="q" autofocus value="{{.Body.Query}}"
       hx-get="/search"
       hx-trigger="keyup changed delay:300ms, search"
       hx-include="[name='kind'],[name='slug'],[name='limit']"
       hx-target="#results"
       hx-swap="innerHTML">
```

Tab clicks update `kind` (a hidden input) and re-fire the search.

Top-nav search box (added in ticket 2): submits via classic `<form action="/search">` GET — falls through to this page.

### Result rendering

- Each hit has a project_slug — render as a small badge so cross-project results are unambiguous.
- For `learnings`, link the parent ticket via `/tickets/{id}?slug={project_slug}` so the slug hint flows through.
- For `comments`, link the parent ticket the same way and anchor to `#comment-{comment_id}` if the detail page renders ids on each comment row (coordinate with ticket 6).
- Show similarity score as a faint right-aligned float (e.g. `0.78`); useful for tuning expectations.

## Key references

- `internal/svc/service.go:SearchProjects,SearchTickets,SearchLearnings,SearchComments`.
- Prior learning (T008 / `54779339`): cross-project resident indexes already aggregate across mounts; results carry `ProjectSlug` natively. No new wiring needed.
- `internal/mcptools/tools.go` — `search_*` handlers as the format reference.

## Hard rules to surface

- Empty query → don't fire a search (htmx `hx-trigger="keyup changed delay:300ms"` plus a small handler-side guard returning the empty-state partial when `q == ""`).
- Search is best-effort over what's currently mounted; if a project isn't mounted, its content won't appear. Add a small "Searching: N mounted projects" hint.

## Gotchas

1. **Limit ceiling**: `Service` enforces max 50; mirror in the handler.
2. **Live search is chatty**: 300 ms debounce + `hx-sync="this:replace"` on the search input avoids overlapping requests.
3. **Empty search vs no results**: distinguish in the partial — empty query renders "Type to search"; non-empty + zero hits renders `search_empty.tmpl`.
4. **`SearchLearnings` and `SearchComments` already accept empty `project_id_or_slug` as "cross-project"** — pass empty string when the `slug` param is missing.
5. **Don't search inside the embedding pipeline** — Service does cosine over the resident index; the request is fast (≤ tens of ms for thousands of entries). Don't wrap it in goroutines.

## Verification

- Manual flow:
  1. `GET /search?q=mcp&kind=learnings` returns hits from past tickets, ranked by similarity.
  2. Switch tab to `Tickets` → re-renders with ticket-shaped results.
  3. Switch tab to `Comments` → comment hits with author + parent ticket link.
  4. Type in the box; results update after ~300 ms; no flicker.
  5. Pass `?slug=tickets-please` → results restricted to that project.
  6. Empty query → "Type to search" empty state.
  7. Click a learning result → lands on the parent ticket detail page with comments anchor working.
- MCP cross-check: `mcp__tickets_please__search_learnings {q}` returns the same top hits in the same order.
- `go test ./internal/web/handlers/search_test.go` — table-driven test over the four kinds.

## Out of scope

- Faceted filters beyond `kind` and `slug` (no date range, no column filter, no author filter).
- Saved searches.
- Highlighting matched terms inside the snippet (nice to have; deferred).
- Type-ahead suggestions.
- Stemming / synonyms (vector search handles semantic similarity already).

## Notes

- Parallelizable with tickets 3, 4, 5 — only depends on ticket 2's layout/sidebar and the top-nav search box slot.
- The top-nav search box (defined in ticket 2's `_nav.tmpl`) is just a plain form with `action="/search"`. After this ticket lands, the nav box becomes alive.
