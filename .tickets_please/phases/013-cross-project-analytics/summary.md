## Phase: Cross-project analytics

Once Phase 1 (modernisation) and Phase 2 (UI refresh) land, the next jump is comparing across projects rather than navigating one at a time. The system already has the raw data — column-move comments record every transition with a timestamp, completions are timestamped + attributed, and the agents store knows who did what. None of it is aggregated.

This phase builds a global analytics surface plus federated search across all mounted projects. Everything is read-only and derived from existing data; no schema changes.

## Goals

- A `/projects` page that lists every mounted project as a row with key headline metrics: ticket velocity (tickets done/week, last 4 weeks), in-progress count, average lead-time (todo → done), average dwell-time per column, completion rate, learnings density.
- Mine column-dwell-time and lead-time by walking `system_move` comments per ticket (their `from_column` / `to_column` pair + `created_at` give the full transition timeline).
- A global agents leaderboard: top agents by tickets-completed in last 30/90/all-time, per-project breakdown.
- Federated search across all per-project vec indexes: one query, hits annotated with project slug, ranked across projects.
- CSV export of the analytics table (one row per project, columns: the headline metrics above) — useful for the data nerd in me.

## Hard rules

- Read-only. No new write paths in this phase.
- Per-project membership/role from Phase 1 W2 gates the analytics view — viewers can only see projects they have membership to. Server-side filter, not just UI hide.
- Federated search re-uses existing per-project vec adapters; no global index. K results per project, merged.

## Out of scope

- Charting beyond inline SVG sparklines. No d3, no chart.js. Single binary stays slim.
- Predictive metrics / forecasts. Just descriptive stats.
- Modifying historical data to backfill anything.

## Waves

```
Wave 1 — Backend: metrics aggregator + dwell-time miner
Wave 2 — Frontend: /projects analytics page + sparklines
Wave 3 — Federation: agents leaderboard, federated search, CSV export
```
