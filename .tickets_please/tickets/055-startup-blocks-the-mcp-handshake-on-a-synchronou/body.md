## Symptom

When the server starts and a mounted project's `project.yaml` declares an
`embed_model` that isn't yet pulled in Ollama, startup tries to `ollama pull`
that model **synchronously, on the boot path**, before the MCP transport is
ready to serve. The pull can take ~90s (or fail with `context deadline
exceeded`). Because the stdio MCP handshake has a hard client-side timeout
(Claude Code: 30s), the client gives up:

```
Failed to reconnect to tickets_please_local: MCP server "tickets_please_local"
connection timed out after 30000ms
```

The server is not hung — it's busy pulling a model — but to the client it's
dead, and the MCP is unusable until the model happens to be present.

## First-hand reproduction (2026-05-29)

Local stdio MCP (`tickets_please mcp`, `DATA_DIR` = this repo's `.tickets_please`),
project declares `embed_model: bge-m3`, Ollama had only `nomic-embed-text`:

```
15:?? INFO  tickets_please starting subcommand=mcp
      INFO  ollama: model not present; pulling model=bge-m3 url=http://localhost:11434
      (~90s blocked)
      WARN  svc: per-mount embed build failed; falling back to server default
            err="... model \"bge-m3\" missing and pull failed: ... context deadline exceeded"
```

90s of dead air on startup → the 30s MCP handshake timed out every time.
`ollama pull bge-m3` out-of-band, then restart → startup reached
`mcp server starting` in ~2s and the client connected fine. So the trigger is
purely "declared model not yet pulled at boot."

This regressed relative to the older binary, which predated the per-project
embedder probe (phase 009) and so never attempted a boot-time pull.

## Why it matters

- Any machine where a project's embed model isn't pre-pulled gets a server that
  "doesn't connect" on first run — with no actionable signal to the user (the
  pull progress goes to the server's stderr, not the client).
- It's worst on a fresh clone / new dev box — exactly when a model is least
  likely to be present.
- The fallback-to-server-default already exists; blocking boot to *first try a
  pull* defeats the point of having a fallback.

## Suspected area

The per-mount embed build / probe path that runs during mount attach at startup
(`internal/svc/service.go` `attachMountEmbedAssets` and the ollama provider
probe in `internal/embed/...`). The probe calls `POST /api/pull` and waits with
a long deadline before falling back.

## Fix options (pick one or combine)

1. **Don't pull on the boot path.** Probe-and-fall-back fast (server default) so
   the MCP comes up immediately; enqueue the desired-model pull + re-embed as
   background work that swaps the embedder in when the model lands. This keeps
   "boot stays accessible," which the per-project-embedder design already values
   (see ticket #51 / the fallback-truth work).
2. **Make the boot probe non-blocking / short-deadline.** Cap the probe at a few
   seconds (well under the 30s handshake), never pull synchronously; surface a
   "model not present, pulling in background" status.
3. **Pull behind an explicit opt-in**, not implicitly at boot (e.g. a Settings
   action or a `--pull-missing` flag).

Recommend (1)+(2): the transport must reach "serving" within a couple seconds
regardless of embedder state; model acquisition is background work.

## Acceptance

- With a project whose declared embed model is absent from Ollama, the MCP
  server reaches its serving/handshake-ready state in < ~5s (no synchronous
  pull on the boot path), and the client connects without timing out.
- Search degrades gracefully meanwhile (server-default embedder or "embeddings
  rebuilding" state); when the desired model becomes available the project
  re-embeds and swaps over without a restart.
- A test that boots a mount whose embedder probe would pull, with a fake/slow
  provider, asserts startup does not block on the pull.

## Provenance

Found while rebuilding this repo's binary from an old (May-5) build and
reconnecting the local stdio MCP; the rebuild pulled in phase-009 code and the
boot-time pull surfaced immediately. Worked around by `ollama pull bge-m3`
before restart. Related: #51 (initial-mount fallback should stamp truth).
