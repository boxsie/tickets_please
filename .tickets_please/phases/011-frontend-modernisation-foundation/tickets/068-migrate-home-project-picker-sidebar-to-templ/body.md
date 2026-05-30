First real migration. Pure 1:1 port — no feature changes, no layout changes beyond what Tailwind classes naturally produce. The point is to prove the new stack works end-to-end on the simplest surface.

## Acceptance

- `internal/web/templates/pages/home.tmpl` reimplemented as `internal/web/components/pages/home.templ`.
- `internal/web/templates/partials/sidebar.tmpl` + `project_picker.tmpl` reimplemented as templ components in `internal/web/components/layout/`.
- Base layout (current `layout.tmpl`) reimplemented in templ; it composes the new sidebar and page slot.
- `handleHome` + `handleSidebarPartial` switch to render the templ versions; the old `html/template` versions stay on disk but unmounted (deleted in [[delete-old-html-template-plumbing]]).
- HTMX `hx-get="/_partials/sidebar"` still works — the new templ partial endpoint returns the equivalent fragment.
- Smoke test: `go test ./internal/web/...` passes; `make run` shows the home page rendered through Tailwind with the new component primitives.

## Hints

- Renderer (`internal/web/render.go`) probably needs a `RenderTempl(w, r, component)` helper that mimics the chrome plumbing. Add it alongside the existing `Render` — don't break old templates yet.
- Sidebar's `sidebar-refresh` event must still fire on project-mutation HX-Trigger — same htmx hook, just rendered via templ now.
