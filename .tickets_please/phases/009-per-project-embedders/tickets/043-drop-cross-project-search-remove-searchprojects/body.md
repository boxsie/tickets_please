## Goal

Cross-project `/search` is broken (project selector misroutes you back to home; you end up searching every project) and meaningless once dims can differ across mounts. Tear it out cleanly.

## Scope

- Delete route at `internal/web/router.go:97`: `mux.Handle("GET /search", wrap(a.handleSearch))`.
- Delete `internal/web/handlers_search.go` entirely.
- Delete `internal/web/templates/pages/search.tmpl`.
- Update top-nav (`internal/web/templates/_nav.tmpl:3`): remove the always-on `<form action="/search">`. Search input reappears in W4-T2 as a per-project form on project pages.
- Delete `internal/svc/search.go`'s `SearchProjects` function (semantic-search-across-summaries) and the `ProjectHit` type that supports it.
- Delete the `search_projects` MCP tool registration in `internal/mcptools/tools.go` and its handler. Bump `totalTools` in `cmd/tickets_please/main.go`, the doc comment in `internal/mcptools/tools.go`, and `expectedTools` in `internal/mcptools/tools_test.go` — these three move in lockstep per prior learnings.
- Existing per-project search service methods (`SearchTickets`, `SearchComments`, `SearchLearnings`) stay — W4-T2 retargets them to mount indexes.

## Tests

- Drop the search e2e tests that hit `/search`.
- `tools_test.go` canonical-list test will fail loud with the new tool count — fix the expected list.
- Web smoke: GET `/search` returns 404.

## Done when

- `make build` + `go test ./...` green.
- `grep -rn '/search\b' internal/web/` returns nothing (W4-T2 brings `/p/{slug}/search` next).
- `mcp__tickets_please__search_projects` returns "tool not found" if any old caller still holds it cached.
