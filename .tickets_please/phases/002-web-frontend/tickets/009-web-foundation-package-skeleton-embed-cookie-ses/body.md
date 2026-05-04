## Goal

Stand up `internal/web/` so `tickets_please serve` answers `GET /` with an HTML page, serves embedded `/static/*`, and attaches a synthetic agent identity to every web request via a signed cookie. This is the foundation every other ticket plugs into.

## Why

Everything else in the phase assumes (a) a place for handlers and templates to live, (b) a working `web.Mount` call site in `runServe`, and (c) per-request agent identity so Service mutations get attributed. Without this ticket none of the resource tickets can land.

## Scope

### New package: `internal/web/`

- `internal/web/deps.go` — `Deps` struct: `Service *svc.Service`, `Logger *slog.Logger`, `Cfg config.Config`, `Dev bool`.
- `internal/web/router.go` — exported `Mount(mux *http.ServeMux, deps Deps)`. Wires `/`, `/static/`, and the future `/p/`, `/tickets/`, `/search`, `/api/*` route groups (stub the resource ones; ticket 2-7 fill them in). Wraps mutating routes in CSRF + session middleware.
- `internal/web/session.go` — cookie middleware:
  1. Read `tp_sid` cookie (HMAC-signed UUID; key derived from `cfg.DataRoot` so it survives restarts but can't be forged across machines).
  2. If absent/invalid: mint UUID; call `deps.Service.RegisterAgent(ctx, "web-ui:"+uuid, "Web UI", map[string]string{"client_name":"Web UI","client_kind":"browser","ua":r.UserAgent()}, 7*24*time.Hour)`; set cookie (`HttpOnly`, `SameSite=Lax`, `Secure` only when `r.TLS != nil`, `Path=/`, `MaxAge=7*24*3600`).
  3. Cache `cookie-uuid → agentID` in `sync.Map` of `*sync.Once` (race-safe first hit).
  4. Inject `svc.WithSessionID(ctx, agentID)` into request context.
  5. On `errors.Is(err, svc.ErrUnauthenticated)` from a downstream Service call (handler reports back via a helper), invalidate cache and re-register transparently.
- `internal/web/render.go` — template loader and helpers:
  - `Page(w, r, name string, data any)` — renders `pages/<name>.tmpl` wrapped in `_layout`.
  - `Partial(w, r, name string, data any)` — renders `partials/<name>.tmpl` (no chrome). Use when `r.Header.Get("HX-Request") == "true"`, otherwise `Page`.
  - `Error(w, r, status int, err error)` — renders `partials/error.tmpl` at the given status (use 422 for hard-rule violations).
  - Builds `*template.Template` from `embed.FS` in prod; in `--dev` mode reparses from `os.DirFS("internal/web/templates")` per request.
- `internal/web/dev.go` — split `prodFS()` vs `devFS()` so the same code path covers both.
- `internal/web/csrf.go` — per-cookie HMAC-signed token, hidden `_csrf` field validator. Helper `CSRF(ctx) string` for templates.
- `internal/web/handlers/home.go` — `GET /`: if `len(svc.ListProjects(ctx)) > 0` redirect to `/p/<first-slug>` (alphabetical for determinism); else render `pages/home.tmpl` with an empty-state hint pointing at `/p/load` (ticket 3 owns the load form, but stub a link).

### Templates and static (minimal, ticket 2 fills in real chrome)

- `internal/web/templates/_layout.tmpl` — minimal `<html>` shell with localhost-only banner, `<link rel="stylesheet" href="/static/app.css">`, `<script src="/static/htmx.min.js" defer></script>`, `{{block "main" .}}{{end}}`.
- `internal/web/templates/pages/home.tmpl` — placeholder content (one `<h1>` and a sentence). Ticket 2 supersedes the layout.
- `internal/web/templates/partials/error.tmpl` — `<div class="error">{{.Message}}</div>` with details list when present.
- `internal/web/static/app.css` — single-line placeholder (ticket 2 ships real Tailwind).
- `internal/web/static/htmx.min.js` — vendored htmx (current 1.x release; commit upstream LICENSE alongside).

### Wiring in `cmd/tickets_please/main.go`

In `runServe` (around line 178-216):
- Add `dev := fs.Bool("dev", false, "developer mode: reparse templates and static from disk instead of embed")` to the `flag.NewFlagSet("serve", ...)` block.
- After the existing `mux.Handle("/mcp", httpMCP)` and `/healthz` lines, add:
  ```go
  web.Mount(mux, web.Deps{Service: s, Logger: log, Cfg: cfg, Dev: *dev})
  ```
- Update the `log.Info("http mcp server starting", ...)` call to also log `"web_ui": true`.

### README

Add a short "Web UI" section: "Open http://localhost:8765/ in a browser. Localhost only — do not expose to a network without putting auth in front of it."

## Key references

- `cmd/tickets_please/main.go:178-216` — `runServe` (insertion site).
- `internal/svc/service.go:RegisterAgent` — synthetic agent registration; signature is `(ctx, agentKey, agentName, metadata map[string]string, requestedTTL time.Duration) (agentID string, expiresAt time.Time, err error)`. Pass `0` to use cfg default if you want; here we pass `7*24*time.Hour` explicitly.
- `internal/svc/middleware.go:WithSessionID` — context-injection helper used by the MCP layer; reuse it.
- `internal/mcptools/tools.go` — read 3-4 handlers (e.g. `register_agent`, `list_tickets`, `move_ticket`) for the param-extract → Service-call → render shape that the web handlers in later tickets will mirror.
- `internal/mcptools/identity.go` — the per-session `Registry` model on the MCP side; the web cookie cache mirrors this concept.

## Gotchas

1. **First-hit RegisterAgent race**: two requests from the same fresh cookie can race. Guard via `sync.Map[string]*sync.Once` keyed by the cookie UUID.
2. **Context propagation**: derive every Service-call context from `r.Context()`, never `context.Background()`, so `srv.Shutdown` cancellation reaches Service.
3. **Cookie key derivation**: `hmac.New(sha256.New, []byte(cfg.DataRoot+"|tp-cookie-v1"))` is fine — it's not a real auth key, just a tamper guard for a localhost UI. Do NOT use `"static-string"` as the key (would let any tickets_please install forge cookies for any other).
4. **Embed paths are relative to the Go file**: `//go:embed templates/* templates/**/* static/*` lives next to the file that declares the `embed.FS` var. Put the var in `render.go` so the embed path resolves correctly.
5. **`/static/` collides with nothing today**, but be explicit: `mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))`.

## Verification

- `go build ./...` clean; `go vet ./...` clean.
- `go test ./internal/web/...` — at minimum: cookie round-trips (set on first hit, reused on second), CSRF token validates round-trip, template loader works in both prod and dev modes.
- `go test -race ./internal/web/...` clean.
- Manually: `tickets_please serve` then `curl -i http://localhost:8765/` returns 200 + `Set-Cookie: tp_sid=...; HttpOnly; SameSite=Lax`. Second request with the cookie returns 200 with no new `Set-Cookie`. `curl http://localhost:8765/static/app.css` returns 200.
- Regression: `curl http://localhost:8765/healthz` still returns `200 ok`; `claude mcp` against `http://localhost:8765/mcp` still works.

## Out of scope

- Sidebar/nav/styling (ticket 2).
- Any resource pages beyond a placeholder home (tickets 3-7).
- Drag-and-drop, SSE, polling, mobile-first layout (deferred).
- Auth, TLS, rate limiting (still localhost-only for the whole phase).

## Notes for the implementing agent

- Vendor `htmx.min.js` (the 1.x line is fine, ~14 KB minified). Commit alongside its `LICENSE` file in `internal/web/static/`.
- Keep this ticket's templates intentionally minimal — ticket 2 will replace `_layout.tmpl` with real chrome. Don't paint yourself into a corner with a layout API that ticket 2 has to undo.
- The `web.Mount` API surface is the contract every other ticket depends on. Try not to change it after this lands; if you must, file the change in the ticket-2 body before starting.
