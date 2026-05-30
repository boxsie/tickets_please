First building block of analytics. Walks each project once, computes a headline metrics struct, caches it. The /projects analytics page reads from this.

## Acceptance

- New `svc.ProjectMetrics(ctx, projectID) (*domain.ProjectMetrics, error)` returning:
  - `Total`, `Active`, `Done`, `Archived` counts.
  - `VelocityLast4Weeks []int` (tickets-done per week, oldest→newest, 4 entries).
  - `InProgressNow` (snapshot).
  - `LeadTimeP50`, `LeadTimeP90` (todo-create → done, in days; computed across the last 90 days of completions, nil if fewer than 5 samples).
  - `CompletionRate` (done / (done + abandoned) — for v1, "abandoned" is approximated as "still in todo after 60 days"; document the heuristic in a comment).
  - `LearningsDensity` (avg learnings char-count per completed ticket, last 90 days).
  - `CommitsPerTicketAvg` (uses the git-index from Phase 3; nil if git linkage disabled for this project).
- Cached per project with a 5-minute TTL; cache invalidated on any ticket mutation in the project.
- `svc.AllProjectMetrics(ctx) (map[projectID]*ProjectMetrics, error)` parallelises across mounted projects.
- Tests cover each metric with seeded ticket data (use a fixture builder).

## Hints

- Velocity uses completed-at week boundaries; weeks start Monday in the user's local TZ (config: `analytics.tz`, default `UTC`).
- LearningsDensity is a proxy for "how much knowledge each ticket left behind" — useful signal.
