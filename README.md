# tickets_please

A ticketing system designed for LLMs first, humans second. Trello-shaped, but every move requires a comment, every completion requires structured testing evidence and learnings, and every word of context is semantically searchable.

> **Status: v1 shipped.** Single binary, 35 MCP tools, filesystem-backed. Hobby project — clone, build, run, point an MCP client at it. See [`SPEC.md`](SPEC.md) for design notes and [`planning/`](planning/) for the work queue (mostly DONE now).

## What makes this different

Most ticket systems pretend humans will read them. This one assumes an LLM will. Two design choices fall out:

- **Every column move requires a comment.** No silent moves. The reason becomes part of the audit trail.
- **Completion is sacred and structured.** Tickets reach `done` only via a dedicated call that requires three fields: testing evidence, work summary, and learnings. The learnings are embedded and become semantically searchable, so future tickets can surface "have I hit this gotcha before?"

The system feeds itself: each completed ticket leaves machine-readable wisdom for the next agent. That's the actual point.

## Architecture in one breath

A single Go binary that runs as an MCP server (stdio by default, optional HTTP `serve` for a long-running multi-repo host). Data lives as a plain directory tree you can `cat`, `grep`, and `git diff`. Embeddings are JSON sidecar files. Every mutation can produce a git commit attributed to the calling agent. No database, no docker, no service to keep alive when running stdio — when the LLM client stops, the binary stops.

## Two ways to run it — and where the tickets live

The same binary supports two storage models. Pick by how you want a project's ticket history to travel:

- **Local service, tickets in the repo.** Run `tickets_please mcp` (stdio) — or `serve` with a `project_path` — and a project's tickets live in **that repo's** `<repo>/.tickets_please/` tree, committed to git. Clone the repo and its full ticket history comes with it; `git diff` shows ticket moves next to the code change that motivated them. This is the default for stdio clients and for anyone who passes `project_path` to `create_project` / `register_agent`. Best when tickets belong to one codebase. (This repo dogfoods exactly this — its own tickets live in `./.tickets_please/`.)

- **Remote service, tickets stored centrally.** Run one long-lived `tickets_please serve` host and bind/create projects by **slug alone** (no `project_path`). The server stores each project under `<remote_project_root>/<slug>` — default `~/.tickets_please/projects/<slug>` **on the server's host**, not in any repo. Ticket history lives with the server, decoupled from any checkout. Best for a shared box serving many repos/people (point several clients at one `serve`). A `serve` client can still pass `project_path` to fall back to in-repo storage for a given project.

Either way, `~/.tickets_please/` also holds user-scoped **agent sessions** and the **mount registry** (which repos the server has seen). Within an in-repo store, only `.staging/` and the `*.embedding.json` sidecars are gitignored.

## Quickstart (5 steps, ~5 minutes)

From a fresh clone:

```sh
# 1. Scaffold the per-repo data dir (.tickets_please/.staging) and the central
#    agent registry (~/.tickets_please/{agents,.staging}).
make init-data

# 2. Drop a sample config at ~/.tickets_please/config.yaml (idempotent).
make init-config

# 3. Start the embedding provider. New projects default to Ollama with bge-m3
#    (8192-token context, 1024-dim); swap to OpenAI in ~/.tickets_please/config.yaml,
#    or override per-project in `.tickets_please/project.yaml`.
ollama serve &
ollama pull bge-m3

# 4. Build the single binary.
make build

# 5. Wire the resulting `./tickets_please` into your MCP-capable client
#    (Claude Desktop / Claude Code / Cursor / etc.) — see snippets below.
```

That's it. The per-repo `.tickets_please/` directory IS the project's data — it's committed to git, so cloning a repo brings its full ticket history with you. Only `.tickets_please/.staging/` is gitignored (transient half-applied writes). The central `~/.tickets_please/` directory holds your agent sessions and the mount registry; it's user-scoped, not committed anywhere.

## Wiring up MCP

### Claude Code (stdio)

```bash
claude mcp add tickets_please /abs/path/to/tickets_please mcp
```

### Claude Code (centralised HTTP)

Run a single long-lived server and point any number of clients at it. One process can host many repos — each session binds to a `project_path` via `register_agent`:

```bash
./tickets_please serve --addr :8765
claude mcp add --transport http tickets_please http://localhost:8765/mcp
```

After connecting, the client must call `register_agent` (an MCP tool) with the absolute `project_path` of the repo it wants to work in; subsequent tool calls then accept `project_id_or_slug` as optional and fall back to that bound project. If the repo has no project yet, call `create_project` first (it's the bootstrap escape valve — no session required) with `project_path` set to the repo root, then `register_agent`.

`/healthz` returns `ok` for liveness probes.

### Install as a background service

To keep the HTTP server (above) running persistently instead of launching it by hand, use the install scripts. Both build the binary, seed `~/.tickets_please/`, register a **per-user** service that runs `serve --addr 127.0.0.1:8765`, health-check it, and print the `claude mcp add` line to wire a client. Re-run any time to update; pass the uninstall flag to remove (your data under `~/.tickets_please/` is left in place).

**Linux / macOS (systemd `--user` service):**

```sh
./install.sh              # install + start
./install.sh --uninstall  # remove
```

**Windows (per-user Scheduled Task, no admin):**

```powershell
powershell -ExecutionPolicy Bypass -File .\install.ps1             # install + start
powershell -ExecutionPolicy Bypass -File .\install.ps1 -Uninstall  # remove
```

Both launch the server windowless and restart it on failure. The Linux service enables systemd *lingering* so it survives logout; the Windows task is triggered at logon. This is the **remote/central** storage model — projects you create against this server land under `~/.tickets_please/projects/<slug>` on the host unless a client binds a specific repo via `project_path`.

### Web UI

The same `serve` process also exposes a browser UI at `http://localhost:8765/`. Open it in any browser — projects, phases, tickets, and search are all reachable. Human edits show up in the audit trail attributed to a `web-ui:<random>` agent (cookie-scoped, valid for 7 days).

**Localhost only — no auth.** Don't expose `:8765` to a network without putting authentication in front of it.

For UI development, run with `--dev` so template and static-asset edits show up on refresh without rebuilding:

```bash
./tickets_please serve --addr :8765 --dev
```

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

> Use the tickets_please MCP to create a project called `demo` (set `project_path` to the absolute path of this repo) with a thoughtful 200+ char summary describing what it's for. Then call `register_agent` against the same `project_path`. Then create a ticket "Wire up the initial board". Move it to `in_progress` with a comment, then complete it with substantive testing evidence, work summary, and learnings.

That single conversation exercises `create_project` (≥200-char summary enforcement, `project_path` bootstrap), `register_agent` (binds the session to the new project so subsequent tool calls don't need `project_id_or_slug`), `create_ticket`, `move_ticket` (comment required, no `done` target), and `complete_ticket` (three structured fields, each ≥10 chars), and populates `search_learnings` for the next agent. Watch `.tickets_please/` fill up with yaml + markdown you can `cat`, `grep`, and `git diff`.

## Tools

The MCP server exposes **35 tools** across eight categories. The full table with per-tool descriptions lives in [`SPEC.md` § MCP server](SPEC.md#mcp-server). At a glance:

| Category | Count | Highlights |
|---|---|---|
| Projects | 8 | `list_projects`, `create_project`, `get_project`, **`get_project_summary`**, `load_project`, `update_project`, `delete_project`, `reembed_project` |
| Phases | 7 | `list_phases`, `create_phase`, `get_phase`, `get_phase_summary`, `update_phase`, `delete_phase`, `list_waves` |
| Tickets | 10 | `list_tickets`, `create_ticket`, `get_ticket`, `update_ticket`, `move_ticket`, `complete_ticket`, `assign_ticket_to_phase`, `delete_ticket`, `archive_ticket`, `unarchive_ticket` |
| Comments | 3 | `add_comment`, `list_comments`, `list_comments_scoped` |
| Search | 3 | `search_tickets`, **`search_learnings`**, `search_comments` |
| Feedback | 1 | **`rate_search_result`** |
| Archive | 1 | `apply_archive_policy` |
| Introspection | 2 | `who_am_i`, **`register_agent`** |

Three tools are load-bearing for LLM ergonomics:

- **`get_project_summary`** — read this before doing any non-trivial work in a project.
- **`search_learnings`** — run this before starting non-trivial work; past you may have left notes.
- **`register_agent`** — HTTP clients must call this once on connect to bind their session to a repo (`project_path`). Stdio clients pre-register at startup.

## Highlights

- **Filesystem-backed.** Projects, phases, tickets, comments, agents — all yaml + markdown files in a normal directory. Hand-edit-friendly. Diffable.
- **Vector search.** Ollama (default, local) or OpenAI. Each project picks its own provider + model in its `project.yaml`; the server-wide config is just the template for newly created projects (currently `bge-m3`). Embeddings live as `*.embedding.json` sidecars next to their source. Brute-force cosine in-memory; pluggable for HNSW.
- **Agent identity.** Every mutating call is attributed. Sessions have a TTL. Audit who-did-what across past work.
- **Phases & waves.** Optional sub-projects for bigger bodies of work; each phase has a ≥200-char markdown summary an LLM can context-load. Waves are a soft int grouping inside a phase or project.
- **Subagent-orchestratable.** Tickets carry `depends_on` / `parallelizable_with` / `blocked_by` (computed) plus `wave`, so a swarm of agents can pick ready work in batches.
- **Concurrency-safe across processes.** Per-project flock for mutations + fsnotify for cross-process cache invalidation. Two MCP clients on the same data dir don't corrupt each other.

## Where to go next

- **[`SPEC.md`](SPEC.md)** — the full design. Every decision, every method signature, every file layout convention, including the complete MCP tool table.
- **[`examples/config.yaml`](examples/config.yaml)** — sample configuration with every key explained. Copy it to `~/.tickets_please/config.yaml` (or run `make init-config`).
- **[`planning/`](planning/)** — the work queue. v1 is ~done; mostly historical now.

## Tech stack

Go · `github.com/mark3labs/mcp-go` · `github.com/knadh/koanf/v2` · `github.com/fsnotify/fsnotify` · `github.com/google/uuid` · `github.com/go-git/go-git/v5` · `gopkg.in/yaml.v3` · Ollama (`bge-m3` by default) for embeddings.

Filesystem storage instead of a database. No protobuf, no gRPC, no docker. One binary, MCP stdio, in-process everything.

## Caveats

This is a personal/exploratory project. Not a product, no SLAs, no support. Built for fun and curiosity. If something here is interesting to you, take it.

## License

MIT — see [LICENSE](LICENSE).
