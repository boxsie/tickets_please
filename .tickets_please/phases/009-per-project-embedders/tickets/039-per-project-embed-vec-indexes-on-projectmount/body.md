## Goal

Each mount carries its own `embed.Provider` + four vec indexes. Different projects can run on different models simultaneously. This is the T11 punt at `internal/svc/service.go:51,80,85`.

## Scope

- `internal/svc/service.go:42-47` — extend `ProjectMount`:
  ```go
  Embed         embed.Provider
  SummaryIdx    *vecindex.Index
  TicketsIdx    *vecindex.Index
  LearningsIdx  *vecindex.Index
  CommentsIdx   *vecindex.Index
  ```
- `internal/svc/service.go:78-87` — delete the four `*Idx` fields from `Service`. Search call-sites (`SearchTickets/Comments/Learnings`) move to per-mount lookup; preserve the existing stdio-fallback to `s.Store` for the single-project case (the registry-empty path that the T010 cross-project work mentions in its learnings).
- Mount build: when a mount is constructed (find the path via `cache.Resolvers` and `NewWithStore` — see "Multi-root project registry" learnings), build the provider from `project.yaml`'s `embed_provider` + `embed_model` (fall back to `s.Cfg` defaults if blank), call `Probe`, allocate the four indexes sized to the probed dim. Stash all of it on the `ProjectMount`.
- Eviction: nil out the indexes the same way `Store` is nilled today. Worker shutdown lands in W2-T2.
- `internal/embed/provider.go:32` — `New(cfg)` becomes `New(view EmbedConfig)` where `EmbedConfig` is a small struct holding `{Provider, Model, OllamaURL, OpenAIKey}`. Project-level provider build uses a merged view (project values win, server fills the gaps).

## Notes

- `vecindex.Entry.Owner` already carries project slug — `RemoveByOwner` exists. Useful for re-embed (W3-T1).
- `Service.EmbedDim` from W1-T1 stays as the **default** dim used for empty-mount fallbacks; per-mount dim wins everywhere it's set.

## Tests

- New `service_per_project_test.go`: spin up two fakes (768d + 1024d) via two project yamls, mount both, confirm independent dims and independent indexes.
- Existing tests probably build single-project services through `NewWithEmbed` — those still work via the cfg fallback.

## Done when

- `make build` + `go test ./...` green.
- Multi-mount fakes prove independent dims; search routed through the right mount returns only that mount's hits.
