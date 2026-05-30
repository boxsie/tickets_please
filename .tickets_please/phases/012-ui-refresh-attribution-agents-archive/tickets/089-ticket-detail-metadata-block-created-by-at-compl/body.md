User: "ticket metadata is lacking" + "author visible on tickets and time". The domain has every field already; the template never renders them.

## Acceptance

- Below the ticket title, render a metadata block (templ component `TicketMetadata(props)`) showing:
  - **Created**: agent name + relative time (hover for absolute ISO).
  - **Updated**: relative time of last `UpdatedAt` (only if different from CreatedAt by >1m).
  - **Completed**: agent + relative time (only if done).
  - **Acting for**: user display name (only if `CreatedFor`/`CompletedFor` set — depends on Phase 1 W2 [[agent-user-bridge]]).
  - **Phase**: link to phase (already in header — dedupe).
  - **Wave**: pill if non-zero.
  - **Archived**: badge if `Archived == true` with `ArchivedAt`.
  - **Dependencies**: count + popover listing each (`Depends.Title` + status badge).
  - **Parallelizable with**: count + popover listing each.
  - **Entry key**: `ticket:{id}` shown in a `<code>` block for copy (paired with a copy button) — useful for `rate_search_result`.
- All times use the shared `reltime` helper from [[shared-reltime-helper]].
- Block is responsive: collapses to a "Details" disclosure on narrow screens.
- Tests cover: each field present when populated; absent when nil; archived state visually distinct.

## Hints

- The popover for deps/parallelizable_with can be a `<details>` + summary; no JS needed.
- "Entry key" is the search-feedback canonical id (`ticket:{uuid}`) — surface it next to the ticket id pill so it's discoverable for human-driven rating in [[search-rating-buttons]].
