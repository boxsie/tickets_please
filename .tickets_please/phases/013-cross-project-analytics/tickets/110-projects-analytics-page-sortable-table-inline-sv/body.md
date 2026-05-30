User: "it would be epic to have a projects page as just being able to analyse all that would be epic". This is it.

## Acceptance

- New route `GET /projects` rendering an analytics dashboard. Sidebar entry (global, above project picker).
- Page is a single sortable table with one row per mounted project the user has membership to. Columns:
  - Project name (link to overview).
  - Total / Active / Done / Archived (small cluster).
  - Velocity sparkline (last 4 weeks, inline SVG — no library).
  - Lead time P50 / P90.
  - In progress now.
  - Completion rate %.
  - Learnings density (chars/ticket).
  - Avg commits/ticket (only if any project has git linkage).
- Click column header to sort; default sort by velocity (this week) desc.
- Below the table, a per-column dwell-time histogram strip (4 mini bar charts, one per column, aggregated across all visible projects) — built from [[column-dwell-time-miner]].
- Each row also gets an expander revealing top 3 most-recent completed tickets in that project (with their lead times).
- Tests cover: handler renders for users with multiple memberships, viewer-only sees only their accessible projects.

## Hints

- Sparkline: 80×24 SVG with a polyline; one per row. Cache the SVG markup per project since metrics are cached.
- Server-side sort — query param `?sort=velocity&dir=desc` — so the table is bookmarkable.
