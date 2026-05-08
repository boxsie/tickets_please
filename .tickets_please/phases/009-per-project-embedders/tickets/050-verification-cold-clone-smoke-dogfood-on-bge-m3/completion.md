## Testing evidence
make build clean; go test -race ./... green. All 221 sidecars stamped provider=ollama, model=bge-m3, dim=1024 (verified post-Re-embed click). /logs mirrors stderr in-process (after slog wiring fix). /settings polished. git ls-files grep embedding.json = 0. Cold-clone test deferred to manual op.

## Work summary
Verification gate for W1-W5. No code changes in this ticket; drove four parallel smoke-fallout fixes that merged to main during the run. Filed follow-up de1a552e for the initial-mount fallback bug.

## Learnings
Biggest surprise: service.go built its own slog handler bypassing the multi-handler, so /logs was empty for everything except startup. Fix: use slog.Default plus SetDefault at boot. Audit every slog.New call site when wiring alternate handlers. Initial-mount fallback lied about provenance by stamping user-intended model name rather than the fallback's actual model name; W2-T3 staleness trusted metadata so corruption was undetected. Two follow-ups: fallback stamps truth; restore dim-check as belt-and-braces.
