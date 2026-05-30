## Goal

Wire the T5 archive decision matrix to a manual MCP tool and an opt-in mount-time sweep, so archival is something the system actually does instead of something it could do if asked.

## Scope

### MCP tool

```
apply_archive_policy({
  project_id_or_slug?: string,
  commit?: false,             // dry-run by default
  limit?: 500                  // cap how many tickets we'll archive in one call
})
```

Returns:

```json
{
  "considered": 1247,
  "would_archive": [
    {"ticket_id": "abc...", "title": "...", "reason": "age>=180d, retrievals=4, dislike_ratio=0.66"},
    ...
  ],
  "archived": [],         // empty unless commit=true
  "skipped": [
    {"ticket_id": "...", "reason": "already archived"},
    ...
  ],
  "config": { ... echoes resolved ArchiveConfig ... }
}
```

When `commit=true`: actually flip the flags, write a `system_archive` comment per ticket via the T5 `ArchiveTicket` path. Comment text: `"Archived by policy: <reason from decide>"`.

If `archive.enabled: false` for the project, return immediately with an explanatory error suggesting the config knob to flip.

### Auto-sweep on mount

When `archive.auto_sweep_on_mount: true`, run the equivalent of `apply_archive_policy({commit: true})` **in the background** after `hydrateMount` completes. Don't block the mount on the sweep — emit a structured log line on completion with counts (`archived=N, considered=M, took=Xms`).

Guards:
- If `archive.enabled: false`, skip silently (config is consistent: you have to opt in to enable, and separately to auto-sweep).
- Coalesce: don't run a sweep if one's already running for this mount (single `sync.Mutex` per mount, `TryLock`-style).
- Don't sweep on cold-clone re-embed flows — the embedding-rebuild background work should complete first. Add a "wait for embed queue idle" guard before running.

### Tool count

Final delta this phase: +4 tools (`rate_search_result` (T2), `archive_ticket` (T5), `unarchive_ticket` (T5), `apply_archive_policy` (T6)). Update `cmd/tickets_please/main.go:totalTools` from current 31 → 35 and `internal/mcptools/tools_test.go:expectedTools` to match. README + SPEC tool tables get rows for all four (the T2/T5 tickets bump in their own scope, this ticket reconciles totals after the last addition).

### Tests

- Dry-run reports `would_archive` without mutating state; second dry-run is idempotent.
- `commit=true` archives the listed tickets; subsequent search excludes them by default.
- `limit` is respected (won't archive more than N in one call).
- `auto_sweep_on_mount: true` actually runs after hydrate (use a fake clock + fixture project).
- Concurrent sweeps on the same mount: second invocation is a no-op-with-warning, no double archive.
- A sweep that runs while a `rate_search_result` call is in flight blocks neither (cross-check the locks).

### Documentation

- README: short subsection in "Workflow reflexes" pointing at archived state and the policy tool.
- SPEC: complete the archive section started in T5 with the sweep + auto-sweep mechanics.
- The `tickets_please init` README template (`cmd/tickets_please/main.go:dataDirReadme`) gets a one-line nod to `feedback.yaml` and the archive policy with a pointer to the SPEC section.

## Out of scope

- Per-mount scheduled sweeps (cron-like). Auto-sweep-on-mount is enough for now; if needed later, a `tickets_please archive --apply` subcommand can drive it from systemd timers.
- Cross-project archive aggregation / global report.
- Unarchive-by-policy (re-promoting things that have started getting hits again). Manual `unarchive_ticket` covers it.

## Critical files

- `internal/svc/archive.go` (new) — `ApplyArchivePolicy(ctx, project, opts) (Report, error)`; wraps the T5 `archive.Decide` helper + `ArchiveTicket`
- `internal/svc/mounts.go` — auto-sweep hook after `hydrateMount`, behind the config flag and the embed-queue-idle guard
- `internal/mcptools/tools.go` — register `apply_archive_policy`
- `internal/mcptools/tools_test.go` — `expectedTools` bump (final reconciliation)
- `cmd/tickets_please/main.go` — `totalTools` = 35; `dataDirReadme` updated
- `README.md` — Workflow reflexes addendum
- `SPEC.md` — archive sweep + auto-sweep mechanics

Depends on T5.
