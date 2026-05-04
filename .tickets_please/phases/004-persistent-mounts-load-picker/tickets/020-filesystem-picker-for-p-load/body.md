## Goal

Replace the "type the absolute path" text input on /p/load with a click-to-navigate filesystem browser served by the same process. The user clicks through their directory tree; folders containing `.tickets_please/project.yaml` are flagged as loadable; one click on "Load this directory" submits to the existing POST /p/load.

## Why

Typing absolute paths is a sharp edge. A real picker turns "load existing project" from a 30-second exercise into 3 clicks.

## Scope

### API endpoint (`internal/web/handlers_fs.go` new file)

`GET /api/fs?path=<abs>` returns JSON:

```json
{
  "cwd": "/home/dan/code",
  "parent": "/home/dan",
  "entries": [
    {"name": "tickets_please", "isDir": true, "hasMarker": true},
    {"name": "some-other-repo", "isDir": true, "hasMarker": false},
    {"name": "scratch", "isDir": true, "hasMarker": false}
  ]
}
```

- Validates `path` is non-empty, absolute, and a directory. Errors → 422 + JSON `{error: "..."}`.
- Default starting `path` if omitted: `os.UserHomeDir()`.
- Filters: only directories (skip files — projects are dirs). Skip dotfile dirs EXCEPT we still set `hasMarker` based on `.tickets_please/project.yaml` existence inside the entry.
- Sort entries alphabetically.
- Cap at 500 entries to bound the response on huge dirs (rare).

### View update (`pages/projects/load.tmpl`)

Two sections:

1. **Picker (default, htmx-driven)**:
   - Breadcrumb of current path (each segment a clickable hx-get link).
   - List of subdirectories with click-to-enter (hx-get on the row, swaps in `partials/fs_picker.tmpl`).
   - Directories with `hasMarker=true` styled with a small "✓ project" badge.
   - "Load this directory" button at the top — disabled with explanatory hint when the cwd lacks a marker; primary submit when it does. Hidden input `path` carries the current cwd.

2. **Manual entry (collapsible `<details>`)**:
   - Existing text input form (kept for power users / scripts).

### Partials

- `partials/fs_picker.tmpl` — entire picker block (breadcrumb + listing + load button). Returned by GET /api/fs when called with `Accept: text/html` OR `HX-Request: true`. JSON for the API form.
- `partials/fs_breadcrumb.tmpl` — clickable parent-path links with `›` separators.

### Routes (`internal/web/router.go`)

- `GET /api/fs` → `wrap(a.handleFSBrowse)` (session middleware applies; CSRF skipped because GET).

## Hard rules

- **Read-only**. No POST /api/fs. No mkdir, no upload.
- **No path normalisation that hides traversal**. Accept `..` in path queries — the user might want to navigate up.
- **Marker check via stat, not read**. Avoid reading project.yaml; only `os.Stat(<entry>/.tickets_please/project.yaml)`.
- **Localhost-only**: no auth, but the existing localhost banner already warns.

## Verification

- `tickets_please serve` running.
- GET /p/load → renders picker rooted at $HOME with subdirectories.
- Click into `code` → htmx swap shows `code/`'s subdirs; one row has the ✓ project badge.
- Click "Load this directory" on a non-project dir → button disabled.
- Click "Load this directory" on a marker-bearing dir → submits POST /p/load → 303 to /p/{slug}.
- Sidebar refreshes via the existing HX-Trigger contract.
- API: `curl 'http://localhost:8765/api/fs?path=/home'` returns valid JSON.
- New Playwright test: load a project via the picker, assert the new project appears in the sidebar.
- `go test ./internal/web/...` green.

## Gotchas

1. **`os.ReadDir` is fast but symlinks resolve** — `entry.Type().IsDir()` checks the symlink target, not the link itself. Fine for our purposes (a symlink to a project dir is a project dir) but worth noting.
2. **HX-Request semantics**: handler returns `partials/fs_picker.tmpl` on htmx, JSON otherwise. Keep both shapes consistent (same data fields).
3. **Path encoding**: paths with spaces / unicode must roundtrip cleanly — use `url.QueryEscape` on the picker's hx-get URLs.
4. **Permission errors**: `os.ReadDir` on an unreadable dir returns an error. Surface inline ("permission denied") rather than 500.
5. **The picker preserves CSRF for the eventual POST** — the form's hidden _csrf input is rendered server-side at /p/load load and isn't refreshed by the htmx swaps. That's fine: the swap only updates the listing, not the form.

## Out of scope

- File preview / inspect.
- Bookmarks / recents.
- Search-as-you-type within the current dir.
- Multi-select / batch mount.

## Notes

- Depends only on the existing /p/load route. Parallel-ready with the registry ticket (different files).
- htmx + minimal markup; no client-side state. The picker is purely server-rendered.
