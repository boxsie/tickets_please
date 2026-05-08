## Per-project embedders + re-embed migrations + Settings UI

The embedding pipeline is currently single-server, single-model, single-dim. Long ticket bodies blow `nomic-embed-text`'s 2048-token cap, and there's no migration path to a longer-context model like `bge-m3`. The fix is per-project embedders: each project's `project.yaml` declares its own `embed_provider` and `embed_model`, sidecars carry that identity inline, and the system auto-migrates (delete + re-enqueue) on config change or cold-clone (sidecars are git-ignored).

## Architecture

- **Per-project embedders.** `ProjectMount` carries its own `embed.Provider` built from project.yaml at mount time; falls back to server defaults for new projects. Different projects can run on different models simultaneously.
- **Per-project vec indexes.** `SummaryIdx` / `TicketsIdx` / `LearningsIdx` / `CommentsIdx` move from `Service` onto `ProjectMount`. The T11 punt at `internal/svc/service.go:51,80,85` lands here.
- **Sidecar metadata.** `*.embedding.json` becomes `{provider, model, dim, vec}`. On hydrate, mismatched provider/model triggers delete + enqueue. Flat-array form is dropped (sidecars are gitignored, disposable).
- **Cold-start = automatic re-embed.** Existing `hydrateMount`'s missing-sidecar enqueue path covers a fresh `git clone`. The "Re-embed" button + `reembed_project` MCP tool are the same routine, just preceded by an `os.Remove` of every sidecar.
- **Search becomes per-project.** Cross-project `/search` is broken (project selector misroutes) and meaningless once dims can differ; drop it. New `/p/{slug}/search` uses the mount's provider for the query vector and the mount's indexes.
- **Settings page** subsumes the existing `/edit` page (name + summary) and adds embed config + Re-embed button. Global page edits server defaults (round-tripped through `yaml.Node` so comments survive).

## Constraint reminders

- Hobby project — lean cuts over backwards-compat noise. Sidecars are disposable.
- `vecindex.Entry.Owner` already carries project slug since T010; `RemoveByOwner` exists. Re-embed can lean on that rather than recreating indexes from scratch.
- `Worker.Flush(ctx)` before any tree-removal is load-bearing — match the pattern used by DeleteTicket / DeleteProject.
- Tool count bookkeeping: `cmd/tickets_please/main.go:totalTools`, `internal/mcptools/tools.go` doc comment, `internal/mcptools/tools_test.go:expectedTools` move in lockstep.

## Out of scope

- True multi-PR rollout of progress streaming for re-embed (a flash + status counter suffices).
- Hot-reload notification across live MCP sessions when global defaults change (only newly-built mounts pick up changes).

Plan file: `/home/dan/.claude/plans/sequential-whistling-sprout.md`.
