## Goal

Survive `systemctl restart`. Today only `cfg.DataDir`'s eager-mount survives; every project loaded via the web UI's Load form vanishes. Add a `<DataRoot>/registry.yaml` that records every successfully-mounted repo path, and re-mount each on startup.

## Scope

### File format

`<DataRoot>/registry.yaml` (alongside `agents/`, `.staging/`):

```yaml
paths:
  - /home/dan/code/tickets_please
  - /home/dan/code/some-other-repo
updated_at: 2026-05-04T18:14:00Z
```

YAML for hand-editability — same convention as project.yaml.

### Service additions (`internal/svc/registry.go` new file)

- `loadMountRegistry(dataRoot)` → `[]string, error`. Returns paths slice (deduped, abs, existing-on-disk). Missing file = empty slice + nil error.
- `saveMountRegistry(dataRoot, paths)` error. Atomic write via tmpfile + rename. updated_at = time.Now().UTC().
- `addToMountRegistry(dataRoot, path)` and `removeFromMountRegistry(dataRoot, path)` — load, mutate, save. Idempotent.

### Service wiring

- `Service.New` after the eager-mount block (around line 214): if `cfg.DataRoot != ""`, load the registry and call `RegisterProjectMount` on each path. Log-and-skip failures (path moved, marker missing). Use a fresh background context.
- `RegisterProjectMount` (around line 274) on success: call `addToMountRegistry(s.Cfg.DataRoot, repoPath)` while holding `mountsMu` (already held).
- `DeleteProject` (in projects.go around line 282) on commit success: drop from the in-memory `projectMounts` map AND call `removeFromMountRegistry(s.Cfg.DataRoot, mount.RepoPath)`.

### Edge cases

- Re-mount idempotency: registry entries that map to already-mounted slugs no-op via `RegisterProjectMount`'s existing dedup.
- Eager-mount duplication: cfg.DataDir's eager-mount runs first and adds itself to the registry (correct — cfg.DataDir is just the default starting repo, not a privileged one).
- Stale entries: a path that no longer has `.tickets_please/project.yaml` returns an error from RegisterProjectMount; log warning, do NOT auto-prune (user might be temporarily-unmounted disk; let them clean up explicitly).
- Empty `cfg.DataRoot`: skip registry entirely (stdio mode without a centralised root has nothing to persist to).

## Hard rules

- Atomic writes only — registry must never be partially-written or corrupt.
- The registry is best-effort persistence, not authoritative state. The on-disk project.yaml in each repo is the source of truth.
- Don't write to the registry from inside a hot path (cache resolution, search) — only on register / delete.

## Verification

- Restart-survival flow:
  1. Start `tickets_please serve --dev`.
  2. Load 2 separate repos via POST /p/load (curl or web UI).
  3. `systemctl --user restart tickets-please`.
  4. GET /p — both projects show in the sidebar without re-loading.
  5. Delete one project via DELETE/POST.
  6. Restart. The deleted project is gone.
- Hand-edit registry.yaml to add a bogus path, restart, confirm log warning + service still starts.
- `go test ./internal/svc/registry_test.go` covers: load-empty-file, load-with-paths, save-then-load roundtrip, add-idempotent, remove-noop-when-absent, atomic-write-survives-mid-write-crash (via t.TempDir + simulated tmpfile rename).
- Cross-check via MCP: `mcp__tickets_please__list_projects` post-restart matches the UI's sidebar.

## Gotchas

1. **Lock ordering**: registry I/O happens UNDER mountsMu (we already hold it during RegisterProjectMount/DeleteProject). Don't acquire any other lock. Registry I/O is fast (small YAML, atomic rename) so holding mountsMu briefly is fine.
2. **Boot-time mount errors must not block startup**: a missing repo on disk should log a warning and skip, not panic. Service must come up with whatever's still loadable.
3. **Auto-mount on register**: when the eager-mount of cfg.DataDir runs, it ALSO writes to the registry. That's intentional — if the user later switches DataDir, the previous one survives.
4. **Don't store relative paths**: enforce `filepath.IsAbs` on every entry both at write time and read time.
5. **DataRoot tilde expansion**: cfg.DataRoot is already tilde-expanded by config.Load (per config.go:130). Don't double-expand.

## Out of scope

- UI for editing the registry (hand-edit YAML or use the existing /p/load + /p/{slug}/delete forms).
- Per-mount metadata (last-touched-at, friendly name) — keep the file dumb.
- Cross-host sync — registry is per-server.

## Notes

- Parallelizable with the picker ticket — they touch different files. Registry changes are pure backend so it should land first.
- This is the long-tail follow-up the web-frontend phase implicitly assumed but never delivered.
