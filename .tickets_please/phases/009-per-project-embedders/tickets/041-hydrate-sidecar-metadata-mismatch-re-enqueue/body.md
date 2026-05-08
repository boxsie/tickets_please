## Goal

When a project's expected embedder differs from what's recorded in a sidecar, treat the sidecar as stale: delete + enqueue. Cold-clone (no sidecars) keeps working through the existing missing-file path.

## Scope

- `internal/svc/hydrate.go:162` — `upsertOrEnqueue` (and the per-kind loaders that call it: `hydrateProjectSummary`, `hydratePhaseSummary`, `hydrateTicketBody`, `hydrateTicketLearnings`, `hydrateTicketComments`) now read sidecars as `vecindex.Sidecar`. Compare `sc.Provider/Model` to the mount's expected pair (from W2-T1). On mismatch (or any decode error → e.g. flat-array legacy format), `os.Remove` the sidecar and call the existing missing-sidecar enqueue path.
- The dim mismatch debug-log at `hydrate.go:171-186` (per the cross-project learnings) becomes a metadata mismatch log: `"sidecar provider/model mismatch, re-embedding"` with `expected=...` and `got=...`.
- Cold-clone path is already handled by the missing-file branch — verify it still fires when no sidecar exists.
- No ProjectMount API changes here; this is internal hydrate plumbing.

## Tests

- Hydrate test that seeds a sidecar with the wrong `provider` field, runs hydrate, and asserts: sidecar gone from disk, source enqueued for re-embed, vec index empty until worker drains.
- Hydrate test for a legacy flat-array sidecar (just a JSON array of floats) — the new `ReadSidecar` returns a decode error → same delete + enqueue path.
- Cold-clone test: tempdir with project.yaml + sources but no sidecars; mount; assert N enqueued jobs.

## Done when

- `make build` + `go test ./...` green.
- Manual: change project.yaml's `embed_model` from `nomic-embed-text` to `bge-m3`, restart server; logs show "sidecar provider/model mismatch" for every existing entry, worker rebuilds.
