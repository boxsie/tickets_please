Surface ideas in the `templ` web UI as a distinct lane/tab, hidden from the default work columns — the visible "home" for spitballs. Reuse the archive UI patterns wholesale (learnings #7e260496 + #adcc2e82).

## Changes (`internal/web/...`)
- **Ideas hidden from work columns by default**: the board/overview `ListTickets` calls already drop ideas (default `IncludeIdeas=false` from the filter ticket). Add a dedicated **Ideas view/lane** that lists `kind=idea` tickets (its own `ListTickets` with kind pinned to ideas).
- **Toggle as a LINK, not a checkbox** — mirror `internal/web/archived_pref.go` / `resolveShowArchived`: a `resolveShowIdeas(w,r)` with `?include_ideas=` > `tp_show_ideas` cookie > false, written back to the cookie, preserving other query params. New shared `ui.IdeasToggle(href, on)` component.
- **Idea pill**: add `attribution.IdeaPill(kind)` as a no-op-when-not-idea component (same shape as `ArchivedPill`), dropped into ticket-reference surfaces so a promoted/shown idea reads as such. Muted/tagged CSS class.
- **Promote button + modal** on idea detail, calling `promote_idea` — model on the archive button (`ArchiveAction`), delegated `dialogs.js` click handler, modal-always-present. Wire the `KindTicketPromoted` SSE event to live-flip the view.
- Thread `ShowIdeas` + toggle href through the relevant page props (overview / search / the new ideas view), same as the archived plumbing.

## Notes
- SSE broadcast limitation from #7e260496 applies (one patch for all subscribers can't know each viewer's cookie pref) — acceptable for homelab, note it.
- Verify with the headless `templ` snapshot harness: board shows the ideation lane separate from todo/in_progress/testing/done.

## Acceptance
- Default board: ideas absent from work columns; ideation lane/view lists them.
- Toggle link flips ideas on/off, preserves other filters, persists via cookie.
- Promote button on an idea moves it into the work board live.
- `make test` (web smoke) green; snapshot shows the lane.
