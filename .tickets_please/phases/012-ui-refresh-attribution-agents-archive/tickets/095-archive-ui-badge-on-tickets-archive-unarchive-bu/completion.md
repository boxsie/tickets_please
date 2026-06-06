## Testing evidence
go test ./... all green (new: archive/unarchive handler happy+require-comment+already-archived+available-when-done; detail badge+button render in active and archived states; component-level ticket-card pill/class). gofmt clean. Live end-to-end against the running systemd server: GET detail shows Archive button + dlg-archive/dlg-unarchive modals; POST /archive → 303, re-fetched detail shows `badge badge-archived` + `>archived<` + Unarchive button; POST /unarchive → 303 back to clean state.

## Work summary
New attribution.ArchivedPill component + .archived muted CSS across cards/wave-rows/search-hits. ArchiveAction button component in detail_live.templ wired into PageActions (both done + non-done). Two always-present archive/unarchive modals in detail.templ. New POST /tickets/{id}/archive|unarchive routes + handleTicketArchive/Unarchive/flipArchive handlers. archiveHref/unarchiveHref helpers. archivedBadgePatch now re-renders PageActions for the live button flip. dialogs.js rewritten as document-level delegation. Committed c53e14b.

## Learnings
Archive UI was a pure surfacing job — svc.ArchiveTicket/UnarchiveTicket, the system_archive/unarchive comment kinds, the eventbus KindTicketArchived/Unarchived events, AND the SSE archivedBadgePatch all already existed (from #081/#082). The web layer just needed buttons, modals, routes, pills.

Key decisions / gotchas:

- SHARED PILL: made attribution.ArchivedPill(archived bool) a no-op-when-false component and dropped it into every ticket-reference surface (TicketCard, phases WaveSection rows, projects SearchResults ticket hits, detail.templ deps/blocks). The attribution package is the natural home — it imports only domain+reltime and is ALREADY imported by all those surfaces for the Chip, so one import covers both. Muted treatment (60% opacity + line-through title) is CSS on a `.archived` class on the container (.ticket-card/.phase-wave-ticket/.search-hit), added via templ.KV. Note: by default archived tickets don't even appear in wave lists/cards/search (svc post-filters them out) until the include-archived toggle (sibling ticket 7e260496) lands, so the pill is latent on those surfaces today — but correct once they show.

- ARCHIVE STAYS ON DONE TICKETS: the freeze rule only covers completion fields, not the archived flag (svc allows it). So the Archive/Unarchive button must render in BOTH the done (FrozenActions) and non-done branches of PageActions. Split it into an exported ArchiveAction(archived) templ component and called it after the if/else inside #ticket-actions so it shows regardless of column.

- LIVE BUTTON FLIP: archivedBadgePatch (SSE) originally only morphed the badge. Extended it to ALSO re-render PageActions (via detailPropsForPatch) so the Archive↔Unarchive button text flips live for other viewers, not just the originating tab (which reloads on the 303 redirect).

- DELEGATION FOR SSE-MORPHED TRIGGERS: dialogs.js bound click handlers per-element at DOMContentLoaded. That breaks the moment an SSE PageActions morph replaces the archive button — the new node has no handler. Rewrote dialogs.js as a single document-level delegated click listener (same pattern as copy.js/agents.js). This is the load-bearing fix for any future SSE-injected dialog trigger, not just archive.

- MODALS ALWAYS PRESENT: both dlg-archive and dlg-unarchive modals are rendered unconditionally (outside the `if !IsDone` block that gates move/complete/delete), so a live button flip always finds its modal in the DOM and done tickets can open them.

- TEST GOTCHA: svc mutation helpers (ArchiveTicket/CompleteTicket) need an authed context — a bare context.Background() gives "unauthenticated: register an agent first". The seedProjectAndTicket helper registers an agent internally but doesn't expose the id, so test helpers that call svc directly (archiveTicket, the done-ticket setup) must RegisterAgent + svc.WithSessionID themselves.

- mustGet(t,client,url) returns NOTHING (just closes the body) — it's not a response accessor. Added a getBody helper that does client.Get + mustReadAll for tests that need the HTML.

- Dogfooding side effect: the live archive→unarchive verification was run against THIS ticket (#095), so the repo has two extra audit commits (archive/unarchive ticket 095). Net archived flag is false. svc archive auto-commits to git under the per-project flock just like move, so live-testing archive on a real ticket leaves commits — use a throwaway ticket next time if that matters.
