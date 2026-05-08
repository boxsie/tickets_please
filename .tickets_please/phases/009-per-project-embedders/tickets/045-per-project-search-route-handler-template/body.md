## Goal

Replace the dropped global `/search` with `/p/{slug}/search` — semantic search scoped to one mount, embedded through that mount's provider, querying that mount's four indexes.

## Scope

- `internal/web/router.go` — add `mux.Handle("GET /p/{slug}/search", wrap(a.handleProjectSearch))`. Place it next to the other `/p/{slug}/*` routes.
- `internal/web/handlers_projects.go` — new `handleProjectSearch`:
  1. Resolve mount via existing helpers.
  2. Read `q` and `kind` (tickets/comments/learnings) from query params.
  3. Embed the query via `mount.Embed.Embed(ctx, q)`.
  4. Call the relevant `Service.SearchTickets/Comments/Learnings` (these get retargeted in this ticket to use the mount's indexes — `domain.SearchTicketsInput` already has a `Slug` field).
  5. Render `pages/projects/search.tmpl` with the results. HX-Request returns just the results fragment.
- `internal/svc/search.go` — rework `SearchTickets/Comments/Learnings` to look up the mount via `s.Cache.Get(slug)` and query `mount.TicketsIdx` etc. directly. Preserve the stdio-fallback to `s.Store` for the single-project case (per the cross-project T010 learnings).
- New `internal/web/templates/pages/projects/search.tmpl`. Three result blocks (tickets/comments/learnings) with kind tabs, modeled on the deleted `pages/search.tmpl` minus the project filter.
- Top-nav (`_nav.tmpl`): when the user is on a project page, render a `<form action="/p/{{ .CurrentSlug }}/search">`; when not, hide the search box. The chrome data needs a `CurrentSlug` field — wire it through.
- MCP tools `search_tickets/search_comments/search_learnings` already require slug for most callers; double-check their handlers route through the mount indexes via the reworked Service methods. Bodies don't need new parameters — slug already in input.

## Tests

- Web smoke: GET `/p/tickets-please/search?q=embed&kind=tickets` returns hits drawn from that mount.
- Two-project test: search in mount A returns no hits owned by mount B's tickets.
- HX-Request returns just the results partial (no chrome).
- `internal/svc/search_test.go` retargeted to per-mount indexes.

## Done when

- `make build` + `go test ./...` green.
- Manually search a project from the web UI; clicking a hit lands on the right ticket/comment/learning, not home.
