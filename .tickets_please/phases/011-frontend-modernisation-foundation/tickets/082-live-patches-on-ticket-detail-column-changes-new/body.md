First user-visible realtime feature. When someone moves a ticket or comments on it, the detail page updates without reload.

## Acceptance

- Ticket detail page subscribes to `ticket:{id}` via Datastar SSE (`data-on-load="@sse('/sse?topics=ticket:{id}')"`).
- On `TicketMoved` event: status badge re-renders, page-actions update (e.g. testing → done changes available actions), a transient toast "Moved to {column} by {agent}" appears.
- On `CommentAdded`: the new comment appended to the thread (no full reload). Use Datastar's element-patch to insert into `#comments-list`.
- On `TicketArchived`/`TicketUnarchived`: archived badge appears/disappears.
- Server-side: a small `internal/web/sse/patch.go` helper renders one templ component and writes it as a `datastar-patch-elements` event keyed by selector.
- Tests cover: mutation publishes → SSE subscriber receives correctly-formatted patch event.
- Manual test plan in the ticket completion: two browser tabs on the same ticket; mutation in tab A appears in tab B within ~100ms.

## Hints

- Datastar's mode `morph` is the right fit for `#comments-list` — append without disturbing scroll.
- Avoid rerendering the whole detail page; emit narrow patches keyed by stable selectors.
