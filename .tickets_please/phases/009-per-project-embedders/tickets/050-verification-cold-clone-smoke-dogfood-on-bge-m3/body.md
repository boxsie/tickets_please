## Goal

Run the end-to-end smoke once the W1–W5 work has landed. Migrate this meta-project's data to bge-m3 in production, fix any hot fallout, and capture concrete testing_evidence.

## Steps

1. `ollama pull bge-m3` on the box hosting the central server.
2. `make build` clean. `go test ./...` clean.
3. Restart the central server (`tickets_please serve --addr :8765 --data-root ~/.tickets_please`). Tail logs while it mounts existing projects:
   - Each mount logs `embed provider=ollama model=<from-project.yaml> dim=<probed>`.
   - Mounts whose project.yaml lacks `embed_*` (existing projects) auto-populate from defaults on first mount and trigger re-embed via the W2-T3 staleness path.
4. Browser → `/p/tickets-please/settings`:
   - Name + summary edit works (regression check vs. dropped `/edit`).
   - Status block shows current embedder + stale count.
   - Click Re-embed → flash, sidecars vanish, worker rebuilds them. Old "input length exceeds the context length" warnings do not return on bge-m3.
   - Change embed_model to `nomic-embed-text` and POST → automatic re-embed (W3-T1 trigger). Status updates.
5. Browser → `/settings`:
   - Edit defaults; submit. `~/.tickets_please/config.yaml` is rewritten with comments preserved.
   - Click Re-embed All → every mounted project rebuilds.
6. MCP smoke: `mcp__tickets_please__reembed_project { project_id_or_slug: "tickets-please" }` returns the expected JSON envelope.
7. Cold-clone test: `git clone` this repo to a tmp dir, point `tickets_please serve --data-root <tmp>` at it. With `*.embedding.json` gitignored (W1-T4) the clone has none on disk. Server logs N enqueued embed jobs on first mount; vectors rebuild without manual intervention; `/p/tickets-please/search?q=embed` returns hits after the worker drains.
8. `git status` after re-embed shows zero `*.embedding.json` churn.

## Done when

All eight steps pass. Capture log excerpts + a minute-marked timeline as `testing_evidence`. Anything that surprised you goes into `learnings`.
