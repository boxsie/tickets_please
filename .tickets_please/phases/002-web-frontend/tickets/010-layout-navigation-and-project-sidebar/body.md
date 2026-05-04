## Goal

Replace the placeholder layout from ticket 1 with the persistent chrome that every later page extends — top bar, project sidebar populated from `svc.ListProjects`, dark theme, error/flash banner, prebuilt Tailwind CSS embedded. After this ticket, the sidebar contract is frozen so tickets 3/4/5/7 can ship in parallel.

## Why

The four resource tickets (projects, phases, tickets, search) all need to render inside the same layout, link into the same sidebar, and reuse the same flash/error patterns. Locking those down here keeps later tickets from stepping on each other's templates.

## Scope

### Templates

- `internal/web/templates/_layout.tmpl` — full HTML shell:
  - `<head>` with viewport meta, favicon, app.css, htmx, optional CSP `<meta>`.
  - `<body>` containing `_nav.tmpl` (top bar) + `_sidebar.tmpl` (left rail) + `<main id="content">{{block "main" .}}{{end}}</main>`.
  - Localhost-only banner at the very top of `<body>` (only rendered when `r.Host` resolves to a non-loopback address — surface a warning, don't break).
- `internal/web/templates/_nav.tmpl` — app title link to `/`, search box (form posts to `/search`, ticket 7 owns the handler), `tp_sid`-derived agent label (e.g. `Web UI · 3a9f`).
- `internal/web/templates/_sidebar.tmpl` — `<ul>` of mounted projects from `pageData.Projects`, each item linking to `/p/<slug>`. Active item gets an `aria-current="page"` attribute and visual highlight (`pageData.CurrentSlug` matches). Below the list: a `[+] load existing project` link to `/p/load` (ticket 3 owns) and `[+] new project` link to `/p/new` (ticket 3 owns).
- Flash banner partial (`partials/flash.tmpl`) consumed by the layout: success/info/error styled rows; flash payload is set via a short-lived cookie (`tp_flash`) read+cleared by `render.Page`.

### Render plumbing

- `internal/web/render.go` — extend the `pageData` payload type:
  ```
  type PageData struct {
      Title       string
      Projects    []domain.Project    // for sidebar
      CurrentSlug string              // for sidebar active state
      AgentLabel  string              // for top bar (e.g. "Web UI · <first 4 chars of cookie uuid>")
      Flash       *Flash              // optional
      Body        any                 // page-specific data
      CSRF        string              // hidden form token
  }
  ```
- `Page` populates `Projects`, `AgentLabel`, `Flash`, `CSRF` automatically via a per-request "context-bag" so handlers only set `Body` (and optionally `Title`, `CurrentSlug`).
- `Partial` skips chrome — no sidebar/nav fetched.

### Static assets

- `internal/web/static/app.css` — prebuilt Tailwind, ~30 KB. Commit the generated file directly (no Tailwind toolchain in the build pipeline). Source `tailwind.config.js` + `input.css` live alongside in `internal/web/static/_src/` so future regenerations are reproducible.
- `internal/web/static/README.md` — paste-able command:
  ```
  npx tailwindcss -i ./_src/input.css -o ./app.css --minify
  ```
  Note: this is only needed when classes change; day-to-day work doesn't touch CSS.
- Choose a dark theme by default (it's a tool for engineers; Trello-board-style cards on a dark background read well). Use Tailwind colour tokens — keep custom CSS to <100 lines.

### Handler tweaks

- `internal/web/handlers/home.go` — keep behaviour from ticket 1 (redirect to first project or show empty state) but render via the new `pageData` shape.
- `internal/web/handlers/load_project.go` (stub) — `GET /p/load`: render `pages/projects/load.tmpl` with a single `<input name="path">` form. Don't implement POST (ticket 3 owns it). This stub exists only so the sidebar link doesn't 404 between this ticket and ticket 3.

## Key references

- `internal/svc/service.go:ListProjects` — returns `[]Project` ordered by mount time. Sort by slug for stable sidebar ordering.
- Ticket 1 `web.Mount` and `render.Page`/`Partial` API surfaces — extend, don't change.

## Gotchas

1. **Localhost-only banner**: detect via `net.ParseIP(r.Host)` + `IsLoopback()` or `r.Host == "localhost:..."`. Reverse proxies in front (e.g. tailscale, cloudflared) will defeat the check — that's fine, the banner is informational not a security boundary.
2. **`pageData.Projects` on every page**: `ListProjects` is cheap (in-memory mount registry) but if it ever isn't, cache for 1s in render. Don't optimise prematurely — measure first.
3. **htmx swaps shouldn't refetch the sidebar**: `Partial` returns `partials/...` only. If a sidebar refresh is wanted (e.g. project create), the handler returns an `HX-Trigger: sidebar-refresh` response header and the sidebar `<aside hx-get="/p" hx-trigger="sidebar-refresh from:body" hx-swap="outerHTML">` swaps itself. Document this contract here so ticket 3 knows to fire the trigger.
4. **Tailwind purge**: include `internal/web/templates/**/*.tmpl` in the `tailwind.config.js` `content` array so unused classes are stripped. Otherwise the bundle balloons.

## Verification

- `go build ./... && go test ./internal/web/...` clean.
- `tickets_please serve`, open `http://localhost:8765/` in a browser:
  - Sidebar lists every project from `list_projects`. Active project highlighted on `/p/<slug>`.
  - Top-nav search box submits to `/search` (404 OK at this stage; ticket 7 fills it in).
  - `[+] new project` and `[+] load existing project` links navigate to stub pages.
  - Localhost banner visible.
- Resize to narrow viewport: sidebar collapses to a hamburger (or stacks); no overflow scrollbars.
- `curl -H "HX-Request: true" http://localhost:8765/` returns the partial only — no `<html>`/`<aside>` tags in the body.
- Regression: `/mcp`, `/healthz`, `/static/*` unchanged.

## Out of scope

- Real project create (ticket 3).
- Per-resource pages beyond placeholders (tickets 3-7).
- Drag-and-drop, real-time updates (deferred).

## Notes

- Don't add a JS framework. htmx + a few CSS utilities only.
- Keep the layout file small enough that ticket 5's board view doesn't fight it.
- If you want to add `Alpine.js` for tiny dropdowns, it's allowed (≈5 KB) — but only if needed; CSS-only `<details>` should cover most cases.
