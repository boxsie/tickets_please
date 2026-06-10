**Observed (remote serves instance, ensemble project):** in the phase list, the phase "Security and Code Quality Review 2026-06-02" shows `0 active / 10 total` but renders **no progress bar at all**, while sibling phases (Service Command Protocol `0 active / 9 total`, Cross-node command UX `1/7`, Observability & Metrics Dashboard `7/7`) all show their bars. Note Service Command Protocol is also 0 active and shows a full green bar, so "all done" alone isn't the trigger.

**Expected:** a fully-done phase renders a full green progress bar like any other.

**Suspected cause (Dan's hypothesis):** related to tickets being in the phase but **not assigned to a wave** (wave 0 / "Unassigned wave"). The phase detail view confirms all 10 of its tickets sit in the Unassigned wave. The progress calculation may be iterating wave buckets and skipping/mis-counting wave 0, leaving the bar with no data to render.

Check the phase-list progress component and whatever aggregates per-phase done/total counts — see whether it derives counts from wave groupings rather than the flat ticket list. Likely shares a root cause with the "phase detail only shows 1 of 19 tickets" bug filed alongside this one, so investigate together.
