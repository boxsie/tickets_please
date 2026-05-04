## Goal

Replace the current /p/{slug} page (which is essentially just a Summary preview + phases card + delete) with a real dashboard. The Summary view (/p/{slug}/summary) stays unchanged — that's the LLM-context surface. The Overview becomes the human-facing "state of play" page.

## Scope

Sections, top to bottom:

1. **Header** — project name + breadcrumb + actions (Open board / Edit / Summary). Same as today.

2. **Metrics strip** — 4 stat cards in a row:
   - Total tickets
   - Active (todo + in_progress + testing)
   - In progress (in_progress only)
   - Done

3. **Status distribution** — a single horizontal stacked bar showing the share of each column with a count + percentage. Color-coded to match the board column dots (todo grey, in_progress blue, testing orange, done green).

4. **Ready to pick up** — top 5 unblocked todo+in_progress tickets sorted by created_at asc (oldest first → things that have been sitting). Click → `/tickets/{id}?slug={slug}`. If empty, hint "All ready work is claimed."

5. **Recent activity** — top 10 tickets sorted by UpdatedAt desc. Each row: title (link), column badge, "updated N ago".

6. **Recent learnings** — top 3 most-recently completed tickets with their learnings excerpt (truncated). The wisdom-search affordance, surfaced at the project level.

7. **Phases at a glance** — table of phases with active/total counts (kept from current page, restyled).

8. **Danger zone** — collapsible delete (kept from current page).

## Implementation

- Backend: extend `projectDetailData` with the dashboard fields. Compute via `ListTickets(limit=200)` + `ListPhases(slug)` then bucket in-handler. `humanizeAgo(time.Time) string` helper for the "N ago" relative timestamps.
- Template: `pages/projects/detail.tmpl` rewritten as a dashboard. Reuse existing `.card`, `.badge`, `.bare-list` classes; new `.metric-card`, `.metric-grid`, `.status-bar`, `.status-bar-segment` classes for the dashboard-specific bits.
- CSS: stat-card grid (4 columns on wide, 2 on tablet, 1 on mobile); horizontal stacked bar with color-coded segments; subtle dividers between sections.

## Verification

- go test ./internal/web/... green.
- Manual smoke against /p/tickets-please: dashboard renders with real numbers; ready list shows actual ready tickets; recent activity reflects the latest moves; recent learnings shows the last 3 completion entries.
- e2e screenshot updated to capture the new shape.

## Out of scope

- Cross-project dashboard at `/`.
- Charts (line/area) for activity trends — single static stacked bar is enough for v1.
- Configurable dashboard widgets / user preferences.
- Realtime updates (would need SSE).

## Notes

- The Summary remains the LLM-loadable canonical doc; the Overview is the human-loadable status page. They serve different audiences and are no longer redundant.
- Single phase-less ticket — small cohesive change.
