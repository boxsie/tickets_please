---
id: T08
title: Embedding providers (Ollama, OpenAI)
status: DONE
owner: subagent-T08
depends_on: [T01]
parallelizable_with: [T02, T03]
wave: 1
files:
  - internal/embed/provider.go
  - internal/embed/ollama.go
  - internal/embed/openai.go
estimate: small
stretch: false
---

# T08 — Embedding providers

## Scope

Define the embedding `Provider` interface and ship two implementations: Ollama (default) and OpenAI.

**In:** Interface + factory + two implementations + a tiny smoketest binary or unit test.

**Out:** No worker, no DB writes, no server wiring. T10 plugs this into the lifecycle.

## Files

- `internal/embed/provider.go`
- `internal/embed/ollama.go`
- `internal/embed/openai.go`
- (optional) `internal/embed/embed_test.go` for the smoketest

## Details

### Interface

```go
type Provider interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    Dim() int
    Name() string  // "ollama" or "openai", for logs
}
```

### Factory

```go
func New(cfg config.Config) (Provider, error)
```

Switches on `cfg.EmbedProvider`. Unknown value → error. Default is `ollama` (already set in T01's config struct).

### Ollama implementation

- POST to `${OLLAMA_URL}/api/embeddings` with `{"model": "nomic-embed-text", "prompt": text}`.
- Response: `{"embedding": [floats...]}`.
- Use `net/http` directly — no need for the official `ollama-go` client (it's a tiny call).
- Set a 60s context-aware timeout; embedding can be slow on first call after model load.
- `Dim() = 768`.

### OpenAI implementation

- Use `github.com/sashabaranov/go-openai`.
- Model: `text-embedding-3-small`.
- Read API key from `cfg.OpenAIKey`. If empty when `EMBED_PROVIDER=openai`, factory returns a clear error.
- `Dim() = 1536`.

### Dim mismatch

The schema in T09 will declare `vector(768)`. T10's worker (or main startup) is responsible for failing loud if `provider.Dim() != 768`. **Don't** put that check in this ticket — keep providers oblivious to schema. Just expose `Dim()`.

## Acceptance criteria

- [ ] `go test ./internal/embed -run TestOllamaSmoke -v` (skipped by default unless `OLLAMA_URL` reachable) calls Ollama and returns a `[]float32` of length 768.
- [ ] OpenAI implementation compiles; a unit test using a fake server confirms request shape and response decoding.
- [ ] Factory: `EMBED_PROVIDER=ollama` returns an `*Ollama`. `EMBED_PROVIDER=openai` without API key returns a clear error.
- [ ] `Name()` returns the right string per provider.

## Notes

See **Embedding pipeline** in the spec. The user-confirmed default is Ollama + `nomic-embed-text` — pick that as the factory default. Keep the implementations small; the cleverness lives in T10 (queueing + sidecar writes) and T11 (in-memory cosine search).
