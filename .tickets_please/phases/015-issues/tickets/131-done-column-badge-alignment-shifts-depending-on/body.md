**Observed:** in ticket list rows, the `DONE` status badge is not in a fixed column — its horizontal position depends on the width of whatever renders to its right (attribution name + date). E.g. in a list where most rows read `Codex Review Ticket Writer · Jun 2`, the one row attributed `Claude Code · Jun 3` has its badge shifted left/differently, so the badge column looks ragged.

**Expected:** the status badge sits in a fixed-width/aligned column regardless of the attribution text length; only the attribution column should absorb the width variation (truncate or right-align it).

**Fix direction:** in the ticket-row layout, give the trailing metadata (attribution + date) a fixed or min width / right-aligned flex basis so the badge column stays vertically aligned across rows, rather than letting the badge float against variable-width content.
