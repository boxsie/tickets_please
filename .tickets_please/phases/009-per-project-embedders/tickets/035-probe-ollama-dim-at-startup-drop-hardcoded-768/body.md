## Goal

Make embedding dim discoverable from the provider, not a constant. This unblocks switching to `bge-m3` (1024d) and any future model.

## Scope

- `internal/embed/ollama.go:17` — delete `const ollamaDim = 768`. Add a `dim int` field on `*Ollama` and a `Probe(ctx context.Context) error` method that runs one `Embed(ctx, "ping")` and stores `len(vec)`.
- `internal/embed/ollama.go:42` — `Dim()` returns the probed value. Panic with a clear message if `Probe` hasn't run (the Service guarantees probe-before-use).
- `internal/embed/openai.go` — mirror the same shape (probe → dim) instead of hardcoding 1536. Same Probe() method on `*OpenAI`.
- `internal/svc/service.go:31` — delete `const expectedEmbedDim = 768`. The service still does one global probe in `New` (Service.EmbedDim field) — per-project providers come in W2; don't refactor too far ahead here.
- `internal/svc/service.go:135` — replace the fixed-768 check with: probe the provider; record `s.EmbedDim`; vec indexes use that. Mismatch is no longer possible (whatever the provider says, that's the dim).

## Tests

- `internal/embed/embed_test.go` — `TestOllamaEmbedSendsModelAndPrompt` already partial-decodes the request body, so it survives. Add a probe test that the fake server returns a 1024-element vec and `Dim()` returns 1024.
- `internal/svc/embed_dim_test.go` — currently asserts `expectedEmbedDim == 768`. Rework to assert that the dim flows from the fake provider through to `Service.EmbedDim`.

## Done when

- `make build` + `go test ./...` green.
- `grep 768 internal/` returns nothing.
- A bge-m3-shaped fake (1024) plus a nomic-shaped fake (768) both work without code changes — only the test setup differs.
