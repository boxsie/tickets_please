# tickets_please

A ticketing system designed for LLMs first, humans second. Trello-shaped, but every move requires a comment, every completion requires structured testing evidence and learnings, and every word of context is semantically searchable.

> **Status: design phase.** The full spec and parallelizable work queue exist. Implementation hasn't started yet — clone the repo to read the design and (eventually) pick up tickets.

## What makes this different

Most ticket systems pretend humans will read them. This one assumes an LLM will. Two design choices fall out:

- **Every column move requires a comment.** No silent moves. The reason becomes part of the audit trail.
- **Completion is sacred and structured.** Tickets reach `done` only via a dedicated call that requires three fields: testing evidence, work summary, and learnings. The learnings are embedded and become semantically searchable, so future tickets can surface "have I hit this gotcha before?"

The system feeds itself: each completed ticket leaves machine-readable wisdom for the next agent. That's the actual point.

## Architecture in one breath

A single Go binary that runs as an MCP stdio server. Data lives as a plain directory tree (`.tickets_please/`) you can `cat`, `grep`, and `git diff`. Embeddings are JSON sidecar files. Every mutation can produce a git commit attributed to the calling agent. No database, no docker, no service to keep alive — when the LLM client stops, the binary stops.

## Highlights

- **Filesystem-backed.** Projects, phases, tickets, comments, agents — all yaml + markdown files in a normal directory. Hand-edit-friendly. Diffable.
- **Vector search.** Ollama (default, local) or OpenAI. Embeddings live as `*.embedding.json` sidecars next to their source. Brute-force cosine in-memory; pluggable for HNSW.
- **Agent identity.** Every mutating call is attributed. Sessions have a TTL. Audit who-did-what across past work.
- **Phases & waves.** Optional sub-projects for bigger bodies of work; each phase has a ≥200-char markdown summary an LLM can context-load. Waves are a soft int grouping inside a phase or project — cluster tickets into ordered batches without committing to hard `depends_on` edges.
- **Subagent-orchestratable.** Tickets carry `depends_on` / `parallelizable_with` / `blocked_by` (computed) plus `wave`, so a swarm of agents can pick ready work in batches without stepping on each other.
- **Concurrency-safe across processes.** Per-project flock for mutations + fsnotify for cross-process cache invalidation. Two MCP clients on the same data dir don't corrupt each other.
- **MCP-native.** ~24 tools tuned for LLM ergonomics, including the load-bearing `search_learnings` and `get_project_summary`.

## Quickstart

```sh
make init-config                  # copies examples/config.yaml -> ~/.tickets_please/config.yaml (idempotent)
make init-data                    # creates .tickets_please/{agents,projects,.staging}
ollama pull nomic-embed-text      # default embedding model (skip if using OpenAI provider)
make build                        # produces ./tickets_please

./tickets_please                  # default subcommand: `mcp` (stdio MCP server)
./tickets_please check            # integrity check + exit
./tickets_please init             # create the data-dir scaffold
```

Then register the resulting `tickets_please` binary with your MCP-capable client
(Claude Desktop, Claude Code, Cursor, etc.) as a stdio MCP server.

The `.tickets_please/` directory IS the data — it's committed to git, so cloning
a repo brings its full ticket history with you. Only `.tickets_please/.staging/`
is gitignored (it holds transient half-applied writes).

## Where to go next

- **[`SPEC.md`](SPEC.md)** — the full design. Every decision, every method signature, every file layout convention.
- **[`planning/`](planning/)** — the work queue. 16 tickets with structured frontmatter (status, deps, files) so subagents can parse and pick up.
- **[`planning/README.md`](planning/README.md)** — orientation, dependency graph, suggested execution waves.
- **[`examples/config.yaml`](examples/config.yaml)** — sample configuration with every key explained.
- **[`.tickets_please/`](.tickets_please/)** — the empty skeleton of the data directory. Once T01 + T02 land, this is where projects/tickets/comments will live.

## Tech stack

Go · `github.com/mark3labs/mcp-go` · `github.com/knadh/koanf/v2` · `github.com/fsnotify/fsnotify` · `github.com/google/uuid` · `github.com/go-git/go-git/v5` · `gopkg.in/yaml.v3` · Ollama (`nomic-embed-text`) for embeddings.

Filesystem storage instead of a database. No protobuf, no gRPC, no docker. One binary, MCP stdio, in-process everything.

## Roadmap

The 16 planning tickets group into waves:

1. **Wave 0** — bootstrap (T01).
2. **Wave 1** — storage primitives, domain types, embedding providers, agent identity (T02, T03, T08, T15).
3. **Wave 2** — project methods + cache, ticket CRUD, comments, vector index (T04, T05, T06, T09).
4. **Wave 3** — move/complete, embedding worker, phases (T07, T10, T16).
5. **Wave 4** — semantic search (T11).
6. **Wave 5** — MCP binary entry point (T12).
7. **Wave 6 (stretch)** — integration tests, polish (T13, T14).

## Caveats

This is a personal/exploratory project. Not a product, no SLAs, no support. Built for fun and curiosity. If something here is interesting to you, take it.
