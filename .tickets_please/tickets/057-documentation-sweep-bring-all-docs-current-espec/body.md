## Goal

Bring every documentation surface up to date with the system as it actually is
today (post phases 001–009), and **rewrite the tickets_please project summary**,
which is the most stale doc of all.

## The project summary is the priority (it describes a system that no longer exists)

`get_project_summary` (and its on-disk twin `.tickets_please/summary.md`)
currently claims, all of which is now wrong or historical:

- "subcommands `mcp` / `init` / `check`" → **`serve` and `migrate` also exist**
  (HTTP transport + legacy-layout flattening). Confirmed: `main.go` has
  `case "mcp" | "serve" | "init" | "check" | "help"` (+ migrate per CLAUDE.md).
- "Currently **stdio-only**" → there is now a long-running **HTTP `serve` mode
  with a browser web UI** at `:8765` (projects/phases/tickets/search/settings).
- "**28 MCP tools**" → the server now registers **~30** (startup logs
  `tools:30`; `tools.go` has ~91 `mcp.NewTool`/related calls — get the exact
  count right).
- "**Per-process Identity singleton** … registered at startup from env" →
  replaced by **per-session identity via the `register_agent` tool**; each
  session binds its own project + model metadata.
- "Embedding via Ollama (default **`nomic-embed-text`**)" → default is now
  **`bge-m3`** (1024-dim, 8192-ctx), and embedders are **per-project**
  (declared in each `project.yaml` as `embed_provider`/`embed_model`, phase 009).
- On-disk layout "`projects/<slug>/{project.yaml,…}`" → the in-tree single-project
  layout was **flattened to the v0.2 shape** (`.tickets_please/{project.yaml,
  tickets/, phases/}` with no `projects/<slug>/` nesting); `migrate` flattens
  legacy stores.
- The entire **"Active pain points" + "Direction"** sections frame
  centralisation / HTTP / per-session identity / Codex-sandbox-Ollama as *future
  work* — it has **shipped**. Rewrite as current architecture, not roadmap.
- References a plan file `~/.claude/plans/so-break-it-down-...md` — drop or
  relocate; the summary shouldn't depend on a path outside the repo.

Rewrite it to a current "what this is / architecture today / how it's wired
(stdio + HTTP serve + web UI) / per-project embedders / constraints" shape.
Update via `update_project(summary=…)` (re-embeds automatically) **and** keep
`.tickets_please/summary.md` in sync (verify whether they're the same file or
two copies — reconcile so there's one source of truth).

## Other doc surfaces to audit (less drift, but check)

- **README.md** — already mentions serve/web-UI/bge-m3/register_agent; verify
  the quickstart, tool list/count, subcommand list, and MCP wiring snippets
  match reality (stdio `claude mcp add … mcp`, HTTP `serve`, web UI). Note the
  local-vs-remote dogfooding nuance if useful.
- **SPEC.md** — densest doc; reconcile the tool table (count + any
  added/renamed tools like `reembed_project`, `search_comments`,
  `list_comments`), the data-layout section (v0.2 flattened shape), and the
  identity/`register_agent` model.
- **CLAUDE.md** (root) — confirm the build/run/conventions and the
  "where THIS repo's tickets live" section still match (it references `migrate`,
  bge-m3, the in-tree store — looks current; verify).
- **examples/config.yaml** — already bge-m3; confirm all keys present in
  `internal/config/config.go` are represented/commented (e.g. per-project embed
  notes, fsnotify/lock knobs).
- **.tickets_please/README.md** (data-dir readme written by `runInit`) —
  verify it still describes the current data-dir layout.

## Acceptance

- Project summary (via `get_project_summary`) accurately describes the
  current architecture: single binary with `mcp`/`serve`/`init`/`check`/`migrate`,
  HTTP serve + web UI, per-session `register_agent` identity, per-project
  embedders defaulting to bge-m3, v0.2 flattened layout, correct tool count.
  No section frames already-shipped work as "Direction/future."
- `.tickets_please/summary.md` matches the MCP summary (single source of truth).
- README / SPEC / CLAUDE.md / examples/config.yaml / data-dir README each
  verified against the code; every concrete number (tool count, subcommands,
  default model, dims) is correct.
- A grep pass for stale tokens returns clean: `nomic-embed-text` as *the*
  default, "stdio-only", "28 tools", "process-singleton" / "Identity singleton",
  `projects/<slug>/` as the in-tree layout.

## Notes

- Pure docs + summary content; no code changes expected (so no test gate beyond
  `make build` staying green). The one "behavioral" step is `update_project` —
  it triggers a re-embed of the summary, which is fine.
- Get exact counts from source, don't guess: `grep -c` the tool registrations
  and read `main.go`'s subcommand switch.
