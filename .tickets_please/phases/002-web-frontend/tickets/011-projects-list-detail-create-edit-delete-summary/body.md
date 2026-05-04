## Goal

Full CRUD on projects via `/p` and `/p/{slug}/...`, mirroring the eight project-related MCP tools (`create_project`, `get_project`, `list_projects`, `update_project`, `delete_project`, `get_project_summary`, `load_project`, `search_projects`). Replaces the ticket-2 stub at `/p/load`.

## Why

Projects are the top-level container. Before tickets/phases/comments are useful, humans need to be able to see which projects are mounted, mount additional ones from disk, edit summary docs, and delete projects that have outlived their purpose.

## Scope

### Routes (handlers/projects.go)

| Method | Path | Purpose | Service call |
|--------|------|---------|--------------|
| GET  | `/p`                 | List view of all mounted projects | `svc.ListProjects` |
| GET  | `/p/new`             | Create form | — |
| POST | `/p`                 | Create project | `svc.CreateProject(slug, name, summary, description)` |
| GET  | `/p/load`            | "Mount existing project from disk" form (replaces ticket-2 stub) | — |
| POST | `/p/load`            | Mount project | `svc.RegisterProjectMount(slug, repoPath)` (slug derived by reading `repoPath/.tickets_please/project.yaml`) |
| GET  | `/p/{slug}`          | Project detail page (header + tabs to phases/board/summary) | `svc.GetProject` + `svc.ListPhases` (for the tab counts) |
| GET  | `/p/{slug}/edit`     | Edit metadata form | `svc.GetProject` |
| POST | `/p/{slug}`          | Update name/description (NOT slug — slug is immutable) | `svc.UpdateProject` |
| POST | `/p/{slug}/delete`   | Delete (with confirmation) | `svc.DeleteProject` |
| GET  | `/p/{slug}/summary`  | Render summary markdown (read + edit) | `svc.GetProjectSummary` |
| POST | `/p/{slug}/summary`  | Update summary markdown | `svc.UpdateProject` (summary field) |

### Templates (under `internal/web/templates/pages/projects/`)

- `index.tmpl` — table or card grid of projects.
- `new.tmpl` — slug + name + description + summary form (server enforces summary ≥200 chars; surface inline).
- `load.tmpl` — single `<input name="path">` field with help text ("absolute path to a directory containing a `.tickets_please/project.yaml` marker file").
- `detail.tmpl` — header with project name/slug, tabs for: Board (`/p/{slug}/board`, ticket 5), Phases (`/p/{slug}/phases`, ticket 4), Summary (`/p/{slug}/summary`).
- `edit.tmpl` — name/description form.
- `summary.tmpl` — rendered markdown view + an "Edit" affordance that swaps in a textarea.
- `partials/project_card.tmpl` — reusable for sidebar refresh and index page.
- `partials/project_summary_view.tmpl` and `..._edit.tmpl` — htmx swap targets for the in-place summary editor.

### Sidebar refresh

When `POST /p` or `POST /p/load` succeeds, set `HX-Trigger: sidebar-refresh` on the response so the sidebar (per ticket 2 contract) re-fetches `/p` and re-renders. Same for `POST /p/{slug}/delete`.

### Markdown rendering

Use a small pure-Go markdown library — `github.com/yuin/goldmark` is already in the dependency tree if any earlier package pulled it; if not, add it. Render summary as sanitised HTML server-side. No client-side JS markdown.

## Key references

- `internal/svc/service.go:CreateProject,GetProject,ListProjects,UpdateProject,DeleteProject,GetProjectSummary,RegisterProjectMount` — these are the targets.
- `internal/mcptools/tools.go` `register_agent` handler — see how it reads `<path>/.tickets_please/project.yaml` to derive slug; mirror that for `POST /p/load`.
- Ticket 2 sidebar contract: `HX-Trigger: sidebar-refresh from:body` on the sidebar `<aside>`.

## Hard rules to surface

- **Summary minimum 200 chars** on create — server returns an error; UI shows inline 422.
- **Slug must be unique and URL-safe** — server validates; UI surfaces conflict.
- **Slug is immutable** post-create — edit form has no slug field.
- **Delete is destructive** — confirmation dialog (browser `confirm()` is fine v1, htmx-confirm or a modal later).

## Gotchas

1. **`load_project` semantics**: Service's `RegisterProjectMount` opens a `Store` rooted at `<repoPath>/.tickets_please/`; if the marker yaml is missing it errors with a clear message — pass that error straight to the UI.
2. **Slug collision across two repos**: `RegisterProjectMount` rejects the second one with "slug 'foo' already mounted at /other/path". Handle the error, show it inline.
3. **Delete cascades**: deleting a project removes all phases/tickets/comments under it. Confirmation must say so loudly.
4. **Sidebar refresh trigger** must be set BEFORE `WriteHeader` — Go's `http.ResponseWriter` ordering matters.
5. **Summary edit race**: optimistic — last write wins. No need for ETag/If-Match in v1.

## Verification

- Manual flow with `tickets_please serve` running:
  1. `GET /p/new`, fill in slug `demo`, name `Demo`, summary (≥200 chars), POST.
  2. Sidebar refreshes and shows `demo`.
  3. Visit `/p/demo` → header shows correct name; tabs render (phases tab links to ticket-4 page, board tab to ticket-5).
  4. Edit name; reload `/p/demo` → reflects.
  5. Edit summary in place via htmx swap; verify rendered markdown updates.
  6. `POST /p/demo/delete` with confirm; sidebar refreshes; project gone.
- MCP cross-check: in another shell, run `mcp__tickets_please__list_projects` after each step — must match UI state byte-for-byte.
- `POST /p/load` with a path containing a `.tickets_please/project.yaml` mounts and shows the new project. With a path missing the marker → inline error.
- Try create with summary <200 chars → 422 partial inline with the message.
- `go test ./internal/web/handlers/...` — handler-level tests for the happy path + each validation error.

## Out of scope

- Project search (ticket 7).
- Phase/ticket UI (tickets 4-6).
- Bulk operations, archive vs delete distinction.

## Notes

- Parallelizable with tickets 4, 5, 7 — they share the layout/sidebar contract from ticket 2 but no template overlap.
- Summary editor is the most polished interaction — getting it right here sets the bar for ticket 5's ticket detail page.
