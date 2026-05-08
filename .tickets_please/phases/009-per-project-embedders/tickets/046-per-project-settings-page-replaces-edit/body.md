## Goal

Project-scoped Settings page replacing `/p/{slug}/edit`. Edit name + summary, edit embed_provider + embed_model, and a Re-embed button.

## Scope

- `internal/web/router.go` — replace `/p/{slug}/edit` (GET form) with:
  ```go
  mux.Handle("GET /p/{slug}/settings",   wrap(a.handleProjectSettings))
  mux.Handle("POST /p/{slug}/settings",  wrap(a.handleProjectSettingsUpdate))
  mux.Handle("POST /p/{slug}/reembed",   wrap(a.handleProjectReembed))
  ```
  Delete the `GET /p/{slug}/edit` line; the existing `POST /p/{slug}` (which `update_project` calls) stays for back-compat with htmx in-place editors but is unused by Settings.
- `internal/web/handlers_projects.go`:
  - `handleProjectSettings` — fold in `handleProjectEditForm`'s name+summary fields. Add `embed_provider` (select: ollama, openai), `embed_model` (text). Status block: "Embedded with `<sc.Provider>/<sc.Model>` (`<dim>d`) — server expects `<expected>`. <N> stale sidecars." Re-embed button: a CSRF'd POST form with `hx-confirm="This will rebuild all embeddings for this project."`.
  - `handleProjectSettingsUpdate` — call `Service.UpdateProject` (which auto-triggers re-embed when embed_* fields change per W3-T1). Flash + redirect.
  - `handleProjectReembed` — call `Service.ReembedProject`, flash "Re-embed enqueued for `<slug>`", redirect to `/p/{slug}/settings`.
- New template `internal/web/templates/pages/projects/settings.tmpl` based on `edit.tmpl`. Delete `edit.tmpl`.
- Project sub-nav: replace the Edit link with Settings. Find the per-project nav (likely in `templates/_nav.tmpl` or a partial); update links and any tests asserting their presence.

## Tests

- Web smoke: GET `/p/tickets-please/settings` 200s, contains the new fields. POST with name/summary changes round-trips. POST with a different embed_model triggers ReembedProject (assert via fake worker queue depth).
- POST `/p/{slug}/reembed` works with CSRF; without CSRF returns 403.
- The old `/edit` URL returns 404 (or a redirect — pick one and document).

## Done when

- `make build` + `go test ./...` green.
- Manual: edit a project's name + summary via Settings, save, see the change. Change embed_model from `nomic-embed-text` to `bge-m3` (after `ollama pull bge-m3`); save; watch the worker rebuild.
