**Observed (remote serves instance, ensemble project):** the expanded phase "TUI UX overhaul" reports `0 active / 19 total` in its header (with a full green progress bar), but the body renders only an "Unassigned wave" group labelled `1 ticket`, containing a single ticket. The other 18 tickets are not displayed anywhere in the phase.

**Expected:** all 19 tickets render, grouped by wave (or all under Unassigned wave if none have a wave).

**Suspected cause (Dan's hypothesis):** tickets being in a phase but **not in a wave** breaks the grouping/render logic — wave-grouped rendering may drop tickets whose wave assignment doesn't match expectations (e.g. only wave 0 renders, or tickets with non-zero waves are bucketed under keys the renderer never iterates). Compare against the "missing progress bar on fully-done phase" issue filed alongside this — both broken phases on ensemble have heavy Unassigned-wave membership, likely one root cause in how phase views bucket tickets by wave.

Repro should be possible locally: create a phase with a mix of wave-0 and waved tickets (and one with 19 wave-0 tickets) and view the phase detail.
