## Goal

Two related quality-of-life features the user asked for after the web-frontend phase shipped:

1. **The server should remember every project it has encountered** so the sidebar isn't empty after a restart. Today, projects loaded via `RegisterProjectMount` (POST /p/load) live only in memory; the only project that survives a restart is the one auto-mounted from `cfg.DataDir` (the host data dir's eager-mount path). Externally-mounted repos disappear on restart and the user has to re-load each by absolute path.

2. **The "Load existing project" form should have a server-side filesystem picker** instead of a single text input asking for an absolute path. Browsers can't read absolute filesystem paths (security feature of `<input type="file">`), but the server runs locally so it can list directories and let the user click-navigate. Each directory entry should flag whether it contains `.tickets_please/project.yaml` so the user can see at a glance which folders are loadable.

## Why

The web UI just landed and is the user's primary interface. Right now the friction is real:
- Restarting the systemd service (e.g. for a new build) clears every loaded project, leaving them with only the dogfood project visible.
- Loading a project means typing `/home/dan/code/whatever` perfectly into a text input — not great UX for browsing.

These two changes turn the UI from "demo-grade" to "I'd actually use this every day."

## Scope

### 1. Persistent mount registry (`~/.tickets_please/registry.yaml`)

- New file `<DataRoot>/registry.yaml` storing `{ paths: [<absolute repo path>, ...], updated_at: <rfc3339> }`. YAML for hand-editability (matches the project file convention).
- On `RegisterProjectMount` success: append the path (idempotent — dedupe by abs path).
- On `DeleteProject` success: remove the path.
- On `Service.New` after the eager-mount of cfg.DataDir: walk registry paths and call `RegisterProjectMount(path)` for each, logging-and-skipping any that fail (path moved/deleted/marker missing). Eager-mount failures don't block service startup.
- New service-level helpers: `loadRegistry()`, `saveRegistry()`, `addToRegistry(path)`, `removeFromRegistry(path)`.
- Concurrency: registry write is rare (mount/delete) and serialized through the existing `mountsMu` so the YAML on-disk lock-step matches in-memory state.

### 2. Filesystem picker for `/p/load`

- New API endpoint `GET /api/fs?path=<abs>` returning JSON: `{cwd, parent, entries: [{name, isDir, hasMarker}]}`. Validates `path` is absolute and a directory. Hidden files filtered out except `.tickets_please/` (which we want to surface as a marker, never as a navigation target).
- Default starting directory: `$HOME` if no `path` supplied.
- New `/p/load` view with two layouts:
  - Picker (default): breadcrumb of the current path, list of subdirectories with click-to-enter behaviour, "Load this directory" button enabled iff the cwd has a `.tickets_please/project.yaml` marker.
  - Manual entry (collapsible): the existing text input + submit, retained for power users.
- Picker uses htmx: clicking a directory name does `hx-get="/api/fs?path=..."` with `hx-target="#picker"` and swaps in a re-rendered partial. No page reload, no JS.
- "Load this directory" submits to existing `POST /p/load` with the path filled in via a hidden input that updates as the user navigates.

### Templates

- Replace `pages/projects/load.tmpl` with the picker layout.
- New `partials/fs_picker.tmpl` — the directory listing fragment swapped in by htmx.
- New `partials/fs_breadcrumb.tmpl` — clickable parent links.

### Handlers

- New `internal/web/handlers_fs.go` with `handleFSBrowse` (GET /api/fs).
- Update `handleLoadProjectForm` (GET /p/load) to render the picker with a sensible starting dir.
- `handleLoadProjectMount` (POST /p/load) unchanged.

## Hard rules

- Filesystem picker is **read-only** — no file mutation, no creating directories. POST routes that mount go through the existing CSRF-checked path.
- Path traversal: every API call validates the path is absolute and the directory exists; we don't normalise away `..` because the user might genuinely want to navigate up.
- The server runs as the user that started `tickets_please serve`, so it can read whatever they can read. Localhost-only posture stands.

## Verification

- After all tickets:
  - `tickets_please serve --dev`, browse to `/`, load 2-3 project repos via the picker.
  - `systemctl --user restart tickets-please.service`. Sidebar still shows all 2-3 projects.
  - Delete one of them via the danger-zone form. Restart. The deleted one is gone from the sidebar.
  - Hand-edit `~/.tickets_please/registry.yaml`, restart, the changes are reflected.
- `go test -race ./internal/{svc,web}/...` green.
- Playwright walkthrough still passes (one new test: load via picker, mount appears in sidebar).

## Out of scope

- Project re-ordering in the registry (alphabetical-by-slug already happens in the sidebar).
- File upload / "create project from a tarball" — manual `tickets_please mcp` from a repo is the create path.
- Multi-user permissions on the registry — single local user, no auth.

## Notes

- Two parallelizable tickets (registry + picker) — registry can land first since it's purely backend; the picker depends only on the existing `/p/load` route.
- After both land, push the polished UI from the web-frontend phase + this phase together as one feature commit.
