Author and time aren't just for the detail page. Every place a ticket is referenced (and there are a lot) should show "· Claude · 2h" beside it.

## Acceptance

- Updated to show author + reltime:
  - `ticket_card.tmpl` (board, lists)
  - Phase-wave-ticket rows on phases-index and phase-detail
  - Ready-to-pick-up list on project overview
  - Recent activity list on project overview
  - Recent learnings list on project overview
  - Search hits (tickets + learnings + comments tabs)
  - Dependencies/Blocks lists on ticket detail
- Format: `Author · relative-time`. Author is the agent name (or user name if the ticket was human-created). Hover = absolute ISO.
- Tests verify the new fields render in each surface.

## Hints

- A shared `TicketAttribution(t)` templ component takes a `*domain.Ticket` and returns the chip.
- For `RecentActivity`, the "Ago" string is already computed server-side — pass the author through alongside it.
