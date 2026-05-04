## Goal

Render the immutable comment audit trail on the ticket detail page and let humans append new comments. Pure addition to the detail page from ticket 5; no surface elsewhere.

## Why

Comments are the primary way humans and agents leave notes on a ticket — and the system enforces append-only semantics so the audit trail never gets rewritten. The web UI must surface that constraint: show every comment, never offer edit/delete, and make adding one a single textarea + submit.

## Scope

### Routes (handlers/comments.go)

| Method | Path | Purpose | Service call |
|--------|------|---------|--------------|
| GET  | `/tickets/{id}/comments`             | (htmx) refresh the comment thread fragment | `svc.ListComments` |
| POST | `/tickets/{id}/comments?slug={slug}` | Add a comment | `svc.CreateComment(id, body)` |

`POST` returns the new `partials/comment.tmpl` fragment so htmx can `hx-swap="beforeend"` it onto the thread without reloading the page.

### Template additions

- `internal/web/templates/partials/comment.tmpl` — single comment row: author label (e.g. `Web UI · 3a9f` or `Claude Code · claude-opus-4-7`), timestamp (relative + absolute on hover), markdown-rendered body, optional system-move badge for `system_move` comments.
- `internal/web/templates/partials/comment_form.tmpl` — textarea + submit button + a small "comments are append-only — no edits, no deletes" hint.
- Extend `internal/web/templates/pages/tickets/detail.tmpl` (slot reserved by ticket 5) to include `<section id="comments">` rendered server-side via `svc.ListComments`, followed by `comment_form.tmpl`.

### htmx wiring

```html
<form hx-post="/tickets/{{.TicketID}}/comments?slug={{.ProjectSlug}}"
      hx-target="#comments-list"
      hx-swap="beforeend"
      hx-on::after-request="if (event.detail.successful) this.reset()">
```

Failure (validation error from Service) → 422 + `partials/error.tmpl` swapped next to the form.

### System comments

- Service emits `system_move` and `system_phase_assign` comments automatically on column moves and phase reassigns. Render them with a distinguishable badge (e.g. small grey pill saying "system") so humans can tell them apart from authored comments.
- Author block on system comments shows the agent that triggered the move (e.g. `Claude Code · claude-opus-4-7` for an LLM, `Web UI · 3a9f` for a human).

## Hard rules to surface

1. **Comments are immutable.** No edit button, no delete button. Markdown source is shown inside a read-only `<pre>` toggle if a user wants to copy it.
2. **No empty comments** — Service rejects; UI form has `required minlength=1`.
3. **No comments on `done` tickets... wait, check this.** Comments may still be allowed on done tickets (the freeze applies to completion fields, not to discussion). Verify against `internal/svc/comments.go`; if Service permits, allow it; if not, surface the error inline. Do NOT silently disable the form.

## Key references

- `internal/svc/service.go:CreateComment,ListComments`.
- `internal/mcptools/tools.go` `add_comment` and `list_comments` handlers — same param-extract pattern.
- Ticket 5's reserved `<section id="comments">` slot in `pages/tickets/detail.tmpl`.

## Gotchas

1. **Order**: render oldest → newest top-to-bottom so `hx-swap="beforeend"` puts the new comment at the bottom (chronological audit trail).
2. **Author resolution**: `comment.AgentID` is a UUID; resolve to a display label via `svc.AgentStore.ReadAgent(id)`. Cache author lookups per request to avoid N+1 file reads on long threads.
3. **System comments lack a body in some cases**? Check actual records on disk — render gracefully (omit the body section, keep the badge + author + timestamp).
4. **htmx form reset on success**: use `hx-on::after-request` per the snippet above; fail silently (keeps the typed text) on 422.
5. **Markdown safety**: same goldmark + sanitiser pipeline used for ticket bodies. Never `template.HTML` raw input.

## Verification

- Open a ticket detail page; comment thread renders chronologically with system-move comments interspersed.
- Add a comment via the textarea; appears at the bottom without page reload; form clears.
- Submit empty → 422 inline.
- Cross-check: `mcp__tickets_please__list_comments {ticket_id}` returns the new comment with `agent_id` matching the `web-ui:<uuid>` identity from the cookie.
- Refresh the page → comment persists.
- No edit/delete UI affordance anywhere.
- `go test ./internal/web/handlers/...` for happy-path + validation-error paths.

## Out of scope

- Comment search (ticket 7).
- @-mentions, reactions, threading.
- Notifications when an agent comments on a ticket you authored.
- Filtering system vs human comments.

## Notes

- Keep the comment form small. It's a textarea, a submit, and a one-line hint — not a rich editor.
- If `<section id="comments">` is empty (no comments yet), render an empty state ("No comments yet. Be the first.").
