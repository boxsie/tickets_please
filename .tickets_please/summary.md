## tickets_please — meta-project (dogfood)

This project tracks the evolution of the `tickets_please` system itself. The system is a single-binary Go MCP server backing a Trello-shaped, LLM-first ticketing model: tickets flow `todo → in_progress → testing → done`, every column move needs a comment, `done` is reachable only via `complete_ticket`, done tickets are frozen, and comments are immutable. Completion is structured and sacred — `testing_evidence` + `work_summary` + `learnings`, all embedded and semantically searchable. That searchable-learnings loop is the whole point.

## Architecture today (v0.3.0)

- **Single Go binary** at `cmd/tickets_please/main.go` (go 1.25.x). Subcommands: `mcp` (stdio MCP server, the default), `serve` (long-running HTTP MCP host + browser web UI), `init`, `check`, and `migrate` (flatten a legacy v0.1 `projects/<slug>/` layout to the v0.2 single-project shape). `help` prints usage.
- **Two transports, one wiring**: stdio via `mcp-go` `ServeStdio`, and HTTP via its `StreamableHTTPServer` mounted at `/mcp` (plus `/healthz` and the web UI under `serve`). MCP library: `github.com/mark3labs/mcp-go` v0.50.0.
- **31 MCP tools** across Projects (8), Phases (7), Tickets (8), Comments (3), Search (3), Introspection (2). The canonical list + descriptions live in SPEC.md § MCP server and are asserted by `mcptools.expectedTools`.
- **Per-session identity via `register_agent`** (not a process singleton). Each session declares its model/client and binds a project — stdio passes `project_path`, remote passes `project_slug`. Stdio pre-registers a default session at startup; HTTP clients call `register_agent` once per connection. Sessions auto-refresh on expiry. Agent records live in the central `~/.tickets_please/agents/`.
- **Per-project embedders.** Each `project.yaml` declares `embed_provider`/`embed_model`; the server default is Ollama **`bge-m3`** (1024-dim, 8192-token context). Each mount probes its provider at attach and sizes its own four vec indexes (summaries/tickets/learnings/comments) to the probed dim. OpenAI is a pluggable alternative. A missing Ollama model is acquired in the **background** — the boot/attach path never blocks on a pull (a slow pull no longer stalls the MCP handshake); the project falls back to the server default, truthfully stamps the model it actually used, and re-embeds when the requested model lands. Sidecars whose stamped provider/model/dim no longer match their mount are treated as stale and rebuilt.
- **On-disk store.** In-tree per-repo layout is the flattened v0.2 shape: `.tickets_please/{project.yaml, summary.md, tickets/<NNN>-<slug>/{ticket.yaml,body.md,completion.md,comments/}, phases/<NNN>-<slug>/...}` plus `*.embedding.json` sidecars (gitignored, regenerable) and `.staging/`. A central data root (`~/.tickets_please/`, key `data_root`) holds user-scoped agent sessions and the cross-repo mount `registry.yaml`. The central/remote multi-project store nests projects under `<remote_project_root>/<slug>/`. Filesystem is the source of truth — no database.
- **Auto-commit per mutation** when the data dir is inside a git repo: every create/move/complete lands as a commit attributed to the calling agent.
- **Cross-process consistency** via `fsnotify` watchers on loaded projects (mtime-polling fallback when `fsnotify_enabled: false`), with an LRU project cache (default 16) and per-project + global advisory file locks (build-tagged: `flock` on Unix, `LockFileEx` on Windows).

## Constraints / ethos

- **Hobby project**, no customers — favor clean cuts and fun tech over backwards-compat noise.
- Single Go binary, no daemons or databases; the file tree is human-readable, greppable, and git-tracked.
- Reuse `mark3labs/mcp-go` primitives (streamable HTTP, per-connection sessions) rather than rebuilding transport.
- This repo **dogfoods its own development**: its tickets live in the in-tree `.tickets_please/` store (project slug `tickets-please`), driven by a local instance — never the remote instance host.
