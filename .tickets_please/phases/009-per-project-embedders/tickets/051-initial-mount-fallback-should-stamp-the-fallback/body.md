## Problem

Surfaced during W6 verification. When a project's `project.yaml` declares an `embed_model` that isn't yet pulled in Ollama, `attachMountEmbedAssets` probes the desired model, fails, then silently falls back to the server-default Embed (e.g. nomic-embed-text). However it leaves `mount.EmbedModel` pinned to the user's *intended* value (e.g. `bge-m3`).

The worker is built with `(provider=fallback, model=mount.EmbedModel)` and stamps `model: "bge-m3"` into every sidecar — even though the actual embedded vector is from the fallback (e.g. 768d nomic data). The sidecar is **lying about its provenance**.

Concrete observation in this repo: 221 sidecars existed with `model=bge-m3, dim=768, provider=ollama` for a couple of hours. After `ollama pull bge-m3` + service restart, the W2-T3 staleness check would NOT have detected the mismatch:

```go
sc.Provider == mount.Embed.Name() // both "ollama" — match
sc.Model    == mount.EmbedModel   // both "bge-m3" — match
// No staleness signal → 768d vectors load into a 1024d index → search returns nonsense.
```

W2-T3 dropped the dim-check on the assumption "provider+model match implies dim match". That assumption breaks when the fallback path lies.

## Fix options (pick one)

1. **Fallback stamps truth.** When `attachMountEmbedAssets` falls back, set `mount.EmbedModel` to the fallback's actual model name (e.g. `cfg.OllamaModel = "nomic-embed-text"`). Sidecars then say `model="nomic-embed-text"`; on next restart with bge-m3 actually pulled, W2-T3 sees the mismatch and auto-rebuilds. Cheapest fix; preserves the "boot stays accessible" property.

2. **Refuse to mount on probe failure.** Hard fail with a structured error; web UI surfaces it as "this project's embedder isn't reachable; pull <model> or fix the URL". Cleaner semantics, locks the project until repaired.

3. **Restore W2-T3's dim-check as a safety net.** Belt-and-braces: even if metadata lies, dim mismatch forces re-embed. Cheap to keep; arguably should never have been dropped.

Recommend (1) + (3) together — the metadata fix is the actual root cause, and the dim-check costs nothing and catches future model-with-different-dim regressions.

## Scope

- `internal/svc/service.go` — `attachMountEmbedAssets` fallback branch sets `mount.EmbedModel` from the fallback `EmbedConfig.Model`.
- `internal/svc/hydrate.go` — re-add `len(sc.Vec) != mount.EmbedDim` as a staleness signal alongside provider/model mismatch.
- New test (`internal/svc/service_test.go` or extend `service_per_project_test.go`):
  - Mount a project whose yaml asks for a model whose probe fails.
  - Worker writes a sidecar; assert `sc.Model` reflects the fallback (truth), not the yaml's intent.
  - Restart the service with the requested model now succeeding (swap `Service.EmbedNew` to a passing fake); hydrate detects the mismatch via provider/model AND/OR dim, and re-enqueues.

## Done when

- `make build` + `go test -race ./...` green.
- Above test asserts the truthful-stamp + auto-rebuild path.
- Existing tests still pass without modification.

## Notes

- Surfacing probe failure in the web UI is W5-T1 / W5-T2 surface-errors territory; this ticket is just about the on-disk truth and staleness detection. The "show fallback in Settings status block" UX is a separate follow-up.
- Pure server-default cfg (yaml has no embed_provider/embed_model) is fine — `defaultEmbedFor(cfg)` already pre-fills the record, so `mount.EmbedModel` matches the actual provider from boot. The lying-stamp scenario only occurs when yaml asks for a *different* model than the server default has.
