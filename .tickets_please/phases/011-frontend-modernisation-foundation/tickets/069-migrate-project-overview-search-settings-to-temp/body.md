Port the project-level pages. Same rule: 1:1, behaviour-identical. Feature changes wait for Phase 2.

## Acceptance

- These page templates ported to templ under `internal/web/components/pages/projects/`:
  - `index.tmpl`, `detail.tmpl`, `summary.tmpl`, `search.tmpl`, `settings.tmpl`, `load.tmpl`, `new.tmpl`
- Search-related partials ported: `project_search_results.tmpl`, `project_picker.tmpl` (already done in [[migrate-home-and-sidebar-to-templ]]), `project_summary_view.tmpl`, `project_summary_edit.tmpl`.
- All metric cards, status bar, ready-list, recent-activity sections on `detail.tmpl` rendered via the new `Card` component.
- Settings page form widgets use the new `Form` component.
- htmx hooks on search input (`hx-get`, `hx-trigger="keyup changed delay:300ms"`) preserved verbatim.
- `go test ./internal/web/...` passes; the smoke tests in `web_smoke_test.go` updated to point at the templ-rendered output where they grep for specific HTML.

## Hints

- The status-bar segments and metric-grid layout are good Tailwind exercises — flex/grid utilities replace the current `.metric-grid` CSS.
- Don't change route paths or query params.
