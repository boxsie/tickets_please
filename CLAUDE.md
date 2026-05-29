# tickets_please

LLM-first, Trello-shaped ticketing system. A single Go binary that runs as an
MCP server (stdio by default; optional `serve` for a long-running HTTP host).
Data is a plain directory tree under `.tickets_please/` you can `cat`, `grep`,
and `git diff`; embeddings are JSON sidecar files. No database, no service to
keep alive in stdio mode. See `SPEC.md` for design and `README.md` for the
quickstart.

## Where THIS repo's own tickets live (read this first)

This repo **dogfoods its own development**. Its tickets are tracked by a
**local tickets_please instance** that stores everything **in this repo** at
`.tickets_please/` (project slug **`tickets-please`**, id
`920db607-6449-4019-826c-28ba39b6a8d0`, ollama/`bge-m3` embeddings).

- **Do NOT use the remote `http://instance/mcp` instance for
  this repo.** That remote hosts the *other* projects (ensemble, jobsworth,
  pug, jukebox-jeff, serves). Filing tickets_please's own bugs/features there is
  wrong — they belong in this repo's in-tree store. (The global `~/.claude.md`
  says "use the tickets_please MCP"; for THIS repo that means the **local**
  instance pointed at this repo, not the remote server.)
- The store is git-tracked (`.tickets_please/**`); `*.embedding.json` and
  `.staging/` are gitignored. Tickets are directories: `ticket.yaml` +
  `body.md` + `comments/` + `completion.md` (+ embedding sidecars). They live
  under `.tickets_please/tickets/` and `.tickets_please/phases/<phase>/...`.
  Ticket numbers are global (max wins); allocate next = highest + 1.
- Prefer driving the local MCP (the binary built from this repo) so number
  allocation, ids, embeddings, and the agent registry stay consistent. Only
  hand-edit the file store when the MCP isn't reachable — and if you do, note it
  on the ticket and expect to reindex embeddings so semantic search finds it.

## Build / run / test

```sh
make init-data     # scaffold .tickets_please/.staging + ~/.tickets_please/{agents,.staging}
make init-config   # drop a sample ~/.tickets_please/config.yaml (idempotent)
make build         # build the single binary (./tickets_please)
make test          # unit tests
make check         # full pre-commit gate (build + test + whatever else it wires)
make run           # run the MCP server (stdio)
```

Go module `tickets_please`, go 1.25.x. End-to-end tests live under `e2e/`.

## Conventions

- Trello columns flow `todo` → `in_progress` → `testing` → `done`. Every column
  move requires a non-empty comment; `done` is reachable only via the dedicated
  completion path; done tickets are frozen; comments are immutable.
- Completion is structured and sacred: `testing_evidence` + `work_summary` +
  `learnings`, all embedded and semantically searchable — that's the point.
- Standard Go style (`gofmt`); wrap errors with context.
