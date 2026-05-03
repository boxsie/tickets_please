# tickets_please

A ticketing system designed for LLMs first, humans second. Trello-shaped, but every move requires a comment, every completion requires structured testing evidence and learnings, and every word of context is semantically searchable.

> **Status: v1 shipped.** Single binary, 29 MCP tools, filesystem-backed. Hobby project — clone, build, run, point an MCP client at it. See [`SPEC.md`](SPEC.md) for design notes and [`planning/`](planning/) for the work queue (mostly DONE now).

## What makes this different

Most ticket systems pretend humans will read them. This one assumes an LLM will. Two design choices fall out:

- **Every column move requires a comment.** No silent moves. The reason becomes part of the audit trail.
- **Completion is sacred and structured.** Tickets reach `done` only via a dedicated call that requires three fields: testing evidence, work summary, and learnings. The learnings are embedded and become semantically searchable, so future tickets can surface "have I hit this gotcha before?"

The system feeds itself: each completed ticket leaves machine-readable wisdom for the next agent. That's the actual point.

## Architecture in one breath

A single Go binary that runs as an MCP stdio server. Data lives as a plain directory tree (`.tickets_please/`) you can `cat`, `grep`, and `git diff`. Embeddings are JSON sidecar files. Every mutation can produce a git commit attributed to the calling agent. No database, no docker, no service to keep alive — when the LLM client stops, the binary stops.

## Quickstart (5 steps, ~5 minutes)

From a fresh clone:

```sh
# 1. Scaffold the local data dir (.tickets_please/{agents,projects,.staging}).
make init-data

# 2. Drop a sample config at ~/.tickets_please/config.yaml (idempotent).
make init-config

# 3. Start the embedding provider. Default is Ollama with nomic-embed-text;
#    swap to OpenAI in ~/.tickets_please/config.yaml if you'd rather.
ollama serve &
ollama pull nomic-embed-text

# 4. Build the single binary.
make build

# 5. Wire the resulting `./tickets_please` into your MCP-capable client
#    (Claude Desktop / Claude Code / Cursor / etc.) — see snippets below.
```

That's it. The `.tickets_please/` directory IS the data — it's committed to git, so cloning a repo brings its full ticket history with you. Only `.tickets_please/.staging/` is gitignored (transient half-applied writes).

## Wiring up MCP

### Claude Code (stdio)

```bash
claude mcp add tickets_please /abs/path/to/tickets_please mcp
```

### Claude Code (centralised HTTP)

Run a single long-lived server and point any number of clients at it:

```bash
./tickets_please serve --addr :8765
claude mcp add --transport http tickets_please http://localhost:8765/mcp
```

`/healthz` returns `ok` for liveness probes.

### Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or the equivalent on your platform:

```json
{
  "mcpServers": {
    "tickets_please": {
      "command": "/abs/path/to/tickets_please",
      "args": ["mcp"],
      "env": {
        "MCP_AGENT_NAME": "Claude Desktop"
      }
    }
  }
}
```

`MCP_AGENT_NAME` shows up in attributions (`created_by`, `completed_by`) so your audit trail tells you which client did what. Override `MCP_AGENT_KEY` too if you want a stable identifier across restarts; the default appends a random hex suffix so two simultaneous MCPs don't collide on the active-session uniqueness check.

### First run

Once the MCP is wired up, ask Claude:

> Use the tickets_please MCP to create a project called `demo` with a thoughtful 200+ char summary describing what it's for. Then create a ticket "Wire up the initial board". Move it to `in_progress` with a comment, then complete it with substantive testing evidence, work summary, and learnings.

That single conversation exercises `create_project` (≥200-char summary enforcement), `create_ticket`, `move_ticket` (comment required, no `done` target), and `complete_ticket` (three structured fields, each ≥10 chars), and populates `search_learnings` for the next agent. Watch `.tickets_please/` fill up with yaml + markdown you can `cat`, `grep`, and `git diff`.

## Tools

The MCP server exposes **29 tools** across six categories. The full table with per-tool descriptions lives in [`SPEC.md` § MCP server](SPEC.md#mcp-server). At a glance:

| Category | Count | Highlights |
|---|---|---|
| Projects | 7 | `list_projects`, `create_project`, `get_project`, **`get_project_summary`**, `load_project`, `update_project`, `delete_project` |
| Phases | 7 | `list_phases`, `create_phase`, `get_phase`, `get_phase_summary`, `update_phase`, `delete_phase`, `list_waves` |
| Tickets | 7 | `list_tickets`, `create_ticket`, `get_ticket`, `update_ticket`, `move_ticket`, `complete_ticket`, `assign_ticket_to_phase` |
| Comments | 2 | `add_comment`, `list_comments` |
| Search | 4 | `search_projects`, `search_tickets`, **`search_learnings`**, `search_comments` |
| Introspection | 1 | `who_am_i` |

Two tools are load-bearing for LLM ergonomics:

- **`get_project_summary`** — read this before doing any non-trivial work in a project.
- **`search_learnings`** — run this before starting non-trivial work; past you may have left notes.

## Highlights

- **Filesystem-backed.** Projects, phases, tickets, comments, agents — all yaml + markdown files in a normal directory. Hand-edit-friendly. Diffable.
- **Vector search.** Ollama (default, local) or OpenAI. Embeddings live as `*.embedding.json` sidecars next to their source. Brute-force cosine in-memory; pluggable for HNSW.
- **Agent identity.** Every mutating call is attributed. Sessions have a TTL. Audit who-did-what across past work.
- **Phases & waves.** Optional sub-projects for bigger bodies of work; each phase has a ≥200-char markdown summary an LLM can context-load. Waves are a soft int grouping inside a phase or project.
- **Subagent-orchestratable.** Tickets carry `depends_on` / `parallelizable_with` / `blocked_by` (computed) plus `wave`, so a swarm of agents can pick ready work in batches.
- **Concurrency-safe across processes.** Per-project flock for mutations + fsnotify for cross-process cache invalidation. Two MCP clients on the same data dir don't corrupt each other.

## Where to go next

- **[`SPEC.md`](SPEC.md)** — the full design. Every decision, every method signature, every file layout convention, including the complete MCP tool table.
- **[`examples/config.yaml`](examples/config.yaml)** — sample configuration with every key explained. Copy it to `~/.tickets_please/config.yaml` (or run `make init-config`).
- **[`planning/`](planning/)** — the work queue. v1 is ~done; mostly historical now.

## Tech stack

Go · `github.com/mark3labs/mcp-go` · `github.com/knadh/koanf/v2` · `github.com/fsnotify/fsnotify` · `github.com/google/uuid` · `github.com/go-git/go-git/v5` · `gopkg.in/yaml.v3` · Ollama (`nomic-embed-text`) for embeddings.

Filesystem storage instead of a database. No protobuf, no gRPC, no docker. One binary, MCP stdio, in-process everything.

## Caveats

This is a personal/exploratory project. Not a product, no SLAs, no support. Built for fun and curiosity. If something here is interesting to you, take it.
