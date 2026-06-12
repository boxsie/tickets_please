## Testing evidence
go test ./internal/web -run 'TestArchived_PhaseDetail|TestPhaseDetail_WaveFocusFilter|TestArchived_PhasesIndex_AllDoneArchivedPhaseKeepsDoneBar'; go test ./internal/web; go test ./...; git diff --check

## Work summary
Phase detail now auto-includes archived tickets when the phase has zero active tickets but a non-zero total, unless include_archived is explicitly set on the request. Added a regression covering an all-done phase with archived completed tickets plus explicit hide behaviour.

## Learnings
When a phase shows 0 active / N total, the phase counts include archived completed tickets but ListTickets hides archived by default. Phase detail needs to auto-include archived for fully inactive phases, while explicit include_archived=false must still win.
