## Goal

Each project's `project.yaml` declares which embedder it uses. Falls back to server defaults when missing.

## Scope

- `internal/store/records.go:17-25` — extend `ProjectRecord`:
  ```go
  EmbedProvider string `yaml:"embed_provider,omitempty"`
  EmbedModel    string `yaml:"embed_model,omitempty"`
  ```
  Dim is **not** stored — it's a property of the model, probed at provider build time.
- `internal/svc/projects.go` — `CreateProject` populates these from `s.Cfg.EmbedProvider` and the model name appropriate to that provider (`s.Cfg.OllamaModel` or hardcoded OpenAI default). `UpdateProject` writes them when the form supplies them.
- `internal/svc/projects.go` — when `UpdateProject` changes either field, the call should still succeed; the actual re-embed migration trigger is W3-T1's job. Just write the new values.
- Reading code that consumes `ProjectRecord` doesn't change yet — W2-T1 is where mounts grow per-project providers.

## Tests

- Extend project create/update tests to assert the new fields are written and round-trip cleanly.
- Existing project.yaml files in this repo lack the fields — `omitempty` keeps them loading; on next `UpdateProject` they'll be filled. Verify with a fixture.

## Done when

- `make build` + `go test ./...` green.
- `cat .tickets_please/project.yaml` after a fresh CreateProject shows `embed_provider:` + `embed_model:` lines.
