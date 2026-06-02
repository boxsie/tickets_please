## Follow-up to tickets-please/078 (acting-for bridge)

The acting-for bridge (`f9bb5d13`) renders comment attribution as **plain text** "Claude (for Dan)". The original acceptance asked for both names to be **linkable** — Claude → `/agents/{id}`, Dan → `/u/{user}` — but those routes don't exist yet (they're Phase-2 W3 deliverables: agents-index/detail pages and a user page). Linking to 404s would be worse UX than plain text, so the links were deliberately deferred.

## What's already in place (so this is pure presentation)

- `domain.Comment.AuthorFor` (UserRef), `domain.Ticket.CreatedFor` / `CompletedFor` are fully persisted + hydrated.
- `buildCommentRow` in `internal/web/handlers_comments.go` already composes the "(for <DisplayName>)" suffix.

## Work (once /agents/{id} and /u/{user} pages exist)

- Replace the plain-text `AuthorLabel` composition with structured props (agent id+name, user id+name) on `pgtickets.CommentRowProps`, and update `comment.templ` to render `<a href="/agents/{id}">Claude</a> (for <a href="/u/{user}">Dan</a>)`. Requires `templ generate`.
- Apply the same to the ticket-detail metadata block's created_by/created_for once that block lands (ticket `8888d70d`).
- Existing comment tests assert on `comment-author`; keep those passing (anchors inside the span are fine).

## Depends on

The agent and user pages from Phase 2 W3 (`ui-refresh-attribution-agents-archive` phase) — without `/agents/{id}` and `/u/{user}` routes the links 404.
