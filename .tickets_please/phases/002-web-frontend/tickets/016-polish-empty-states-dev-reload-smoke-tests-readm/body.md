## Goal

Final pass to make the UI feel finished and document its operation. Everything should work after tickets 1-7; this ticket is about consistency, dev ergonomics, and proof.

## Why

Tickets 3-7 each ship a vertical slice and were intentionally lean on cross-cutting polish. This ticket is the cleanup pass that ties them together so the phase can be marked done with confidence.

## Scope

### 1. Consistent empty states

Audit every list/grid/thread:

- `/p` (no projects) — "No projects mounted. [Load existing] [Create new]"
- `/p/{slug}/board` (no tickets) — "No tickets yet. [Create one]"
- `/p/{slug}/board` per-column (column empty) — small grey "—"
- `/p/{slug}/phases` (no phases) — "Phaseless project. Create a phase to organise larger bodies of work."
- Comment thread (no comments) — already done in ticket 6; verify wording.
- Search results (zero hits) — already done in ticket 7; verify.

Use one shared `partials/empty_state.tmpl` with `{Icon, Title, Body, Actions}`.

### 2. Loading indicators

- Add htmx `<div id="htmx-indicator" class="hidden htmx-indicator">` to `_layout.tmpl` and a single `htmx.min.css`-equivalent rule in `app.css` so requests >300 ms show a subtle progress bar at the top.
- Per-form spinners on the move/complete/comment forms via `hx-indicator`.

### 3. Consistent error styling

- Audit `partials/error.tmpl` usage across all handlers; ensure colour/spacing/copy is consistent.
- 404s: replace stdlib default with a friendly page (`pages/_404.tmpl`).
- 500s: same (`pages/_500.tmpl`); log the error server-side, show a generic message client-side.

### 4. Dev mode

- Implement `internal/web/dev.go`'s `--dev` template + static reparse path (skeleton from ticket 1).
- Verify: edit a `.tmpl` while `tickets_please serve --dev` is running; refresh browser; new content appears.
- Static asset serving in dev: read from `os.DirFS("internal/web/static")` so editing `app.css` shows up on reload.
- Document the workflow in `internal/web/README.md` (new file): how to add a page, how to add a partial, how to debug htmx swaps.

### 5. Smoke tests

`internal/web/web_smoke_test.go` (new):

- Spin up `web.Mount` over an in-memory `*svc.Service` (mirror the setup pattern used by `internal/mcptools/integration_test.go` — temp dir, `svc.New(cfg)` with a tempdir DataRoot, register a fake project mount).
- Wrap with `httptest.NewServer`.
- Test cases:
  1. `GET /` returns 200 with `Set-Cookie: tp_sid=...`.
  2. `GET /static/app.css` returns 200, content-type `text/css`.
  3. `GET /healthz` (regression check the existing route survives mounting).
  4. `POST /p` with valid form creates a project; `mcp__svc.ListProjects` reflects it.
  5. `POST /p/{slug}/tickets` then `POST /tickets/{id}/move` with a comment moves the ticket; columns reflect.
  6. `POST /tickets/{id}/move` with target=done returns 422.
  7. `POST /tickets/{id}/complete` with all three fields succeeds; ticket is in `done` and frozen (subsequent `POST /tickets/{id}` returns 422).
  8. `GET /search?q=...&kind=tickets` returns >0 hits after seeding.

These run in CI via `go test ./internal/web/...` — keep total runtime under 5 s.

### 6. README + SPEC updates

- `README.md`: add a "Web UI" section after "Wiring up MCP":
  - Open `http://localhost:8765/` in a browser.
  - Localhost-only warning; no auth.
  - Brief screenshot reference (commit a small `docs/web-ui.png` if you can render one; otherwise prose only).
  - Audit-trail note: human edits show up as `web-ui:<uuid>` agents in the ticket history; cookies live for 7 days.
  - `--dev` mode for template iteration.
- `SPEC.md`: add a one-paragraph "Web UI" subsection under the existing "Centralised mode" section pointing at the new code path. Note that the UI is `html/template` + htmx, single binary, no separate process.

### 7. Audit `web-ui:*` agent identity

- Verify human writes show up correctly in the audit trail by:
  1. Adding a comment from the UI.
  2. Running `mcp__tickets_please__list_comments {ticket_id}` and confirming the agent reads `Web UI · <uuid suffix>` not `unknown`.
  3. Confirming `who_am_i` over MCP shows the LLM agent (not the web one) — the two identities don't bleed into each other.

## Key references

- `internal/mcptools/integration_test.go` (or whichever existing test wires up an in-memory Service end-to-end) — copy the bootstrap pattern.
- All ticket 1-7 handlers and templates.

## Gotchas

1. **Smoke tests must clean up tempdirs** — use `t.TempDir()` exclusively; don't leak `~/.tickets_please` artefacts.
2. **`--dev` template reparse is per-request expensive** — it's fine for development but never default to dev=true. Document loud and clear.
3. **Don't introduce real auth here** — out of scope; leave that as a v2 ticket if/when needed.
4. **`docs/web-ui.png`**: only commit if you can render the UI; otherwise skip the screenshot rather than ship a placeholder.

## Verification

- `go test ./...` green; `go test -race ./internal/web/...` green; total web-test wall clock < 5 s.
- `tickets_please serve --dev`: edit `_layout.tmpl`, refresh, see change.
- `tickets_please serve` (default): edit `_layout.tmpl`, refresh, no change (embed is frozen at build time).
- Manual click-through across every page; consistent empty states; loading indicator visible on slow operations (synthesise with `time.Sleep` if needed for the demo).
- README "Web UI" section renders correctly on GitHub (check headings, code fences, image link if added).
- Audit trail check: comment from UI shows up with `web-ui:` agent identity; LLM comment from a parallel MCP session shows the LLM identity.

## Out of scope

- Drag-and-drop board (separate v2 ticket).
- Real-time push (SSE/WebSocket — separate v2 ticket).
- Authentication / multi-user accounts (separate v2 phase).
- Mobile-first redesign.

## Notes

- This is the gating ticket. Mark it done only after every page passes the manual click-through *and* the smoke test suite is green.
- Capture any drift you find between the phase's promised behaviour and what shipped — file follow-up tickets via `create_ticket` rather than scope-creep into this one.
- Suggested learnings to leave in `complete_ticket`: anything you discovered about htmx + Go template ergonomics that future agents would want to know; anything about the Service API that was awkward to call from HTTP-handler context (so the next phase, if it adds auth or push, has a head start).
