See phase summary for full design. Implement in one PR.

## Files

- `internal/web/render.go` — add `URL string` to `Chrome`, add `hasSuffix` template helper.
- `internal/web/router.go` — chrome assembly sets `Chrome.URL = r.URL.Path`.
- `internal/web/templates/partials/sidebar.tmpl` — restructure end-to-end.
- `internal/web/templates/partials/project_picker.tmpl` — new combobox partial.
- `internal/web/static/_src/input.css` — picker + project-nav classes.
- `internal/web/static/_src/tailwind.config.js` — safelist new classnames.
- `e2e/tests/walkthrough.spec.ts` — three new screenshots.

## Verification

- `go test -race ./internal/web/...` green.
- Manual: navigate board → ticket → phases; per-project nav stays visible; correct item highlighted at each step.
- Picker opens on click, search filters in real time, click outside closes.
- Playwright walkthrough captures the new sidebar shape.

## Gotchas

1. `Chrome.URL` includes query string in `r.URL.Path`? No, `r.URL.Path` is path-only. Good.
2. The `<details>` open state persists across page navigation? No — each navigation re-renders chrome from scratch and the details starts closed. That's the desired UX.
3. The htmx sidebar-refresh swap (`hx-get="/_partials/sidebar"`) re-renders the WHOLE sidebar — so opening the picker, deleting a project, then the sidebar refreshes will close the picker. That's fine: the user just deleted a project, they're done with the picker.
4. Active project highlighting: when on `/p/{slug}/board`, the picker label should show the project name. Chrome already has `Projects` list; match against `CurrentSlug`.
5. The picker's search input should NOT submit the page on Enter — wrap in a non-form `<div>` rather than a `<form>`.
