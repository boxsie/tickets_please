---
id: T06
title: CommentService
status: TODO
owner: ""
depends_on: [T02, T03, T04, T15]
parallelizable_with: [T05, T08]
wave: 2
files:
  - internal/svc/comments.go
  - internal/store/comments.go
estimate: small
stretch: false
---

# T06 — CommentService

## Scope

Implement `CommentService.{CreateComment, ListComments}`. User comments only at this stage; T07's transactional code emits `system_move` and `system_completion` files alongside, and `ListComments` already needs to surface them, so the list reads all kinds.

**In:** `internal/svc/comments.go`, store helpers in `internal/store/comments.go`.

**Out:** No update/delete (immutable per spec). No system-comment generation — that's T07.

## Files

- `internal/svc/comments.go`
- Extensions to `internal/store/comments.go`

## Details

### Comment file naming

`<created_at_compact>-<short-id>-<kind>.md` inside `tickets/<NNN>-…/comments/`, where:
- `created_at_compact = time.Now().UTC().Format("20060102T150405.000000000Z")` — sortable; nanoseconds prevent collisions.
- `short-id = first 8 hex chars of comment uuid`.
- `kind = user | system_move | system_completion`.

This guarantees `ls` ordering matches creation order.

### Comment file content

YAML frontmatter + markdown body:

```markdown
---
id: <uuid>
kind: user
author_id: <agent uuid or null>
from_column: null
to_column: null
created_at: 2026-05-02T14:11:09.000Z
---
<body markdown>
```

Use the frontmatter codec from T02 (`internal/store/frontmatter.go`).

### `CreateComment(ticket_id, body)`

1. Validate body non-empty (after trim).
2. Lazy-load the project.
3. Verify `loaded.Tickets[ticket_id]` exists (`NotFound` otherwise).
4. Build a `Comment` with `kind = user`, `author_id = agent.id` from context.
5. `StageOp` writing the comment file.
6. Commit. Caption: `[tickets_please] comment on <slug>/<NNN> [<agent>]`.
7. Append into `loaded.Comments[ticket_id]`.
8. Enqueue `JobComment` (T10 marker).
9. Return the `Comment`.

### `ListComments(ticket_id)`

1. Lazy-load.
2. Read from `loaded.Comments[ticket_id]`. If unloaded for some reason, walk the comments dir on disk and rehydrate.
3. Order: chronological (filenames already sort).
4. Surface all kinds.
5. Hydrate `author` (`AgentRef`) by looking up `loaded.Project` agents map / `s.Store.GetAgent(author_id)`. If lookup fails, leave `author` zero-valued.

## Acceptance criteria

- [ ] `Service.CreateComment` with empty body → `domain.ErrInvalidArgument`.
- [ ] `CreateComment` against a non-existent ticket → `domain.ErrNotFound`.
- [ ] `ListComments` returns the comment with `Kind = CommentKindUser` and a populated `Author`.
- [ ] After T07 lands, `ListComments` interleaves user + system comments in created_at order.
- [ ] Two comments created in rapid succession produce distinct filenames (nanosecond timestamp + short-id avoids collision).
- [ ] No `UpdateComment` or `DeleteComment` method exists on `Service`.

## Notes

See **Service API > Comments**, **Data layout > Comment filename convention**, and **Design decisions > Comments are immutable** in [`../SPEC.md`](../SPEC.md). The body of `system_completion` comments is a formatted summary — T07 owns that format; this ticket only writes user comments.
