## Goal

Top-level `/settings` page editing server defaults (defaults for *new* projects, plus shared transport bits like `ollama_url`), plus a "Re-embed all projects" button.

## Scope

- `internal/web/router.go` — add:
  ```go
  mux.Handle("GET /settings",                wrap(a.handleGlobalSettings))
  mux.Handle("POST /settings",               wrap(a.handleGlobalSettingsUpdate))
  mux.Handle("POST /settings/reembed-all",   wrap(a.handleReembedAll))
  ```
- New `internal/web/handlers_settings.go`:
  - `handleGlobalSettings` — render with current values from `s.Cfg`: default `embed_provider` (select), default `embed_model` (text), `ollama_url` (text), `openai_api_key` (password input with masked existing value), `data_dir` (read-only), and a table of mounted projects (slug, embed_provider, embed_model, sidecar-staleness count, per-project Re-embed link to `/p/{slug}/reembed`). Re-embed-all button at the bottom.
  - `handleGlobalSettingsUpdate` — call `config.SaveYAMLNode(path, ...)` from W5-T2 to mutate just the targeted scalar nodes (preserves comments). Update `s.Cfg` under a mutex (the chrome already shows the current values from cfg, so no further refresh needed). Defaults only affect *new* projects, so no provider rebuild is required for live mounts.
  - `handleReembedAll` — call `Service.ReembedAllProjects`, flash "Re-embed enqueued for N projects", redirect.
- New template `internal/web/templates/pages/settings.tmpl`. Top-nav (`_nav.tmpl`) gets a Settings link visible everywhere.

## Notes

- Hot-reload of the live mounted providers when defaults change is **out of scope** — defaults only matter for new projects. Existing projects override via their own `project.yaml`. Per-project Re-embed via Settings handles model migrations.
- Keep `cfg.OpenAIKey` masked: render `••••••••` if a value exists; only update the file if the form value is non-empty (treat blank as "leave unchanged").

## Tests

- Web smoke: GET `/settings` 200s with current cfg values + project table. POST changes round-trip to disk; assert comment preservation via a fixture.
- POST `/settings/reembed-all` with two mounts queues both.
- Masked OpenAI key: blank submit doesn't wipe the existing key.

## Done when

- `make build` + `go test ./...` green.
- Manual: `/settings` lets you flip the default embed_model and the next CreateProject picks it up; existing projects' table shows their per-project embedders.
