Archived tickets are hidden by default everywhere. Add the toggle that brings them back.

## Acceptance

- `/p/{slug}/search` gains a checkbox "Include archived" that adds `&include_archived=true` to the query; passes through to the existing `search_*` service methods which already support the param.
- `list_tickets` (used by phase-detail, project overview ready/recent lists) accepts an `include_archived` query param on the page; same checkbox shown at the top of those surfaces (default off).
- When `include_archived=true`, archived tickets render in-place with the muted styling from [[archive-ui-badge-actions]].
- Toggle state persisted per-user in a cookie (`tp_show_archived=1`) so it doesn't reset on navigation.
- Tests cover: param flows through to the service call; muted styling applied.

## Hints

- Don't add the checkbox to ticket-detail — that page already shows archived tickets unconditionally (per MCP spec).
- The cookie is per-user, not per-project — one global preference.
