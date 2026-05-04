## Phase: Web frontend

Bundle a browser-facing UI directly into the existing `tickets_please serve` HTTP server so humans can use the system without an MCP client. The frontend attaches to the *already running* serve process — no new daemon, no second port, no separate datastore. Same `*svc.Service` instance backs both `/mcp` (LLM clients) and the new web routes (humans).

## Why now

The centralised HTTP server (`centralise-mcp-server` phase, all 8 tickets done) is stable. Until now every interaction with tickets_please has been LLM-mediated; humans browsing or fixing data go straight to YAML on disk. A read/write web UI closes that gap and makes the audit trail (column moves, completion learnings, comments) actually viewable by the people who care about it.

## Decided design

- **Attach mode**: bundle into `serve` binary. New routes registered on the same `http.ServeMux` at `cmd/tickets_please/main.go` (~line 209) — `/` (UI), `/static/*` (assets), `/api/*` (REST for htmx). Single process, single port (`:8765`).
- **UI tech**: Go `html/template` + htmx, all assets `go:embed`-ed. No Node toolchain, no build step. Tailwind shipped as a small prebuilt CSS file committed to the repo.
- **Scope**: full CRUD parity with the 29 MCP tools. Browse projects/phases/tickets/comments; create/update/move/complete tickets; add comments; cross-project search.
- **Auth**: none. Localhost-only, same posture as `/mcp`. README + UI banner make this explicit.
- **Identity for human writes**: synthetic agent per browser, registered lazily via `svc.RegisterAgent` with `client_name="Web UI"`; agent_id stored in a signed `tp_sid` cookie. Web mutations show up in the audit trail attributed to `web-ui:<uuid>`.

## Architecture

### Package layout (new)

```
internal/web/
├── router.go      Mount(mux *http.ServeMux, deps Deps)
├── deps.go        Deps{Service *svc.Service, Logger *slog.Logger, Cfg config.Config, Dev bool}
├── session.go     cookie middleware → synthetic agent → svc.WithSessionID(ctx)
├── render.go      template parse + content-negotiated Page/Partial/Error helpers
├── dev.go         --dev: os.DirFS instead of embed.FS, reparse per request
├── handlers/
│   ├── home.go projects.go phases.go tickets.go comments.go search.go
├── templates/
│   ├── _layout.tmpl _nav.tmpl _sidebar.tmpl
│   ├── pages/{home,projects/*,phases/*,tickets/*,search}.tmpl
│   └── partials/{ticket_card,move_form,complete_form,comment,error,...}.tmpl
└── static/
    ├── htmx.min.js  app.css  favicon.svg  README.md (regen instructions)
```

### Service reuse

Web handlers call `*svc.Service` methods directly (no MCP round-trip). Service is already concurrency-safe — it serves `/mcp` on the same process via `mountsMu`, per-store flock, `Cache` locks. Multi-project routing happens inside Service (`ResolveProjectStore`, `hostStoreForTicket`); web handlers just pass the URL slug. Pattern mirror is `internal/mcptools/tools.go` — handlers are thin: param-extract → Service-call → render.

### Identity / sessions

Cookie middleware (`session.go`):
1. Read `tp_sid` cookie (HMAC-signed UUID, key derived from `cfg.DataRoot`).
2. If absent or invalid: mint UUID; `svc.RegisterAgent("web-ui:"+uuid, "Web UI", {client_name:"Web UI", ...}, 7d)`; set cookie (`HttpOnly`, `SameSite=Lax`, `Secure` only behind TLS).
3. Cache `cookie-uuid → agentID` in an in-process `sync.Map` of `*sync.Once` to avoid first-hit race.
4. Inject `svc.WithSessionID(ctx, agentID)` into request context. Re-register transparently on `ErrUnauthenticated`.

### htmx pattern

Same handler, content-negotiated on `HX-Request: true`:
- Normal nav → `pages/foo.tmpl` wrapped in `_layout`.
- htmx swap → `partials/foo.tmpl` (no chrome).
- Service domain errors (hard-rule violations, etc.) → HTTP 422 + `partials/error.tmpl` swapped inline.

### CSRF

Per-session HMAC-signed token in a hidden `_csrf` field on every form. No external dep.

## Hard rules to surface in UI

The SPEC's invariants must be enforced server-side (Service already does) and surfaced clearly client-side:

1. Every column move requires a non-empty comment → move form has required `<textarea>`.
2. `done` is only reachable via `complete_ticket` → move-to-`done` action redirects to a completion form with three required textareas (testing_evidence, work_summary, learnings; `minlength=10`).
3. `done` tickets are frozen → detail page disables all action buttons with explanatory tooltip.
4. Comments are immutable → no edit/delete UI affordance, "comments are append-only" hint.

## Out of scope for this phase

- Auth (still localhost-only; bearer-token middleware can bolt on later via mcp-go's middleware path).
- TLS (terminate via reverse proxy when home-server happens).
- Drag-and-drop board interactions (button-based moves first; native HTML5 DnD as a later polish ticket).
- Real-time updates (no SSE/WebSocket push; htmx polling on the board if needed, deferred).
- Mobile-first layout (desktop-first; mobile later).
- Multi-user accounts / per-user agents (single anonymous web agent per browser cookie).

## Tickets in this phase

Numbered 1-8 in execution order. Foundation (1-2) serialises; resource tickets (3-7) parallelise; polish (8) is last.

```
1 Foundation (skeleton + cookie session)
└─ 2 Layout + sidebar
   ├─ 3 Projects CRUD       ┐
   ├─ 4 Phases + waves      │  parallel
   ├─ 5 Tickets board       │  (5a/5b split optional)
   │   └─ 6 Comments thread │
   └─ 7 Cross-project search┘
                              └─ 8 Polish + smoke tests + README
```

## Reference

- Plan: `/home/dan/.claude/plans/lets-use-the-tickets-foamy-parasol.md`
- Critical files: `cmd/tickets_please/main.go` (line ~209), `internal/svc/service.go`, `internal/svc/middleware.go`, `internal/svc/agents.go`, `internal/mcptools/tools.go`, `internal/mcptools/identity.go`.
- Prior learning (from `centralise-mcp-server`/T007): mcp-go's `NewStreamableHTTPServer` returns an `http.Handler`; mount directly with `mux.Handle`. No wrapper needed for our new routes either.
