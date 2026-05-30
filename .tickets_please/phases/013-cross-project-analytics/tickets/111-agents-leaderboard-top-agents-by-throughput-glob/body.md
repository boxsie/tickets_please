Who's shipping the most? Layered on top of the [[svc-listagents-counts]] aggregates from Phase 2, but with proper time-windowed slicing.

## Acceptance

- New section on `/agents` index page: "Leaderboard" with tabs for `Last 30 days` / `Last 90 days` / `All time`.
- Columns: rank, agent name, tickets completed, comments authored, lines of code shipped (sum of insertions from commits attributed to this agent in the window, requires git linkage).
- Per-project breakdown: each agent row expands to show "of which X in tickets-please, Y in foo, Z in bar" (only projects the viewing user has membership to).
- Backed by new `svc.AgentLeaderboard(ctx, window time.Duration) ([]AgentRanking, error)`.
- Default tab: 30 days.
- Tests cover ranking math + windowed filter + per-project breakdown.

## Hints

- Reuse `Service.AgentActivity` from [[svc-listagents-counts]] — windowed throughput is just counting activity items within the window.
- LOC shipped requires the git-index from Phase 3; gracefully omit the column if no project has git linkage configured.
