## Goal

`*.embedding.json` files need to carry the embedder identity that produced them, so the hydrate layer can detect "wrong embedder" and re-enqueue. Today they're a flat JSON array with no metadata (`internal/vecindex/persist.go:12,27`).

## New format

```json
{"provider":"ollama","model":"bge-m3","dim":1024,"vec":[...]}
```

## Scope

- `internal/vecindex/persist.go` — define `type Sidecar struct { Provider, Model string; Dim int; Vec []float32 }`. Replace `WriteSidecar(path, vec)` with `WriteSidecar(path, sc Sidecar)`. Replace `ReadSidecar(path) ([]float32, error)` with `ReadSidecar(path) (Sidecar, error)`. No back-compat reader for the flat-array form — sidecars are gitignored and disposable, cold clone rebuilds them.
- All callers update: `internal/worker/embed.go:218` (`WriteSidecar` after embed) and every hydrate loader that calls `ReadSidecar` (find via `grep -rn ReadSidecar internal/`).
- Hydrate logic in W2-T3 will use `sc.Provider/Model` for staleness; for this ticket just plumb the field through (read it, ignore for now).

## Tests

- New unit in `internal/vecindex/persist_test.go`: round-trip a Sidecar through Write/Read on a tempdir; assert all four fields survive.
- Existing tests that build sidecars by hand may need helper updates — search for `WriteSidecar(` in tests.

## Done when

- `make build` + `go test ./...` green.
- A re-embed-from-scratch produces sidecars in the new shape; old flat-array sidecars are unreadable and force a re-embed (which is exactly what we want).
