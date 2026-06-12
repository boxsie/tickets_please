## Testing evidence
Ran focused web regression tests: GOCACHE=/tmp/tickets-please-gocache go test ./internal/web -run 'TestArchived_PhasesIndex_AllDoneArchivedPhaseKeepsDoneBar|TestArchived_PhaseDetail_TogglesVisibility|TestPhasesIndex_EnrichesWithWaves'. Ran full web package: GOCACHE=/tmp/tickets-please-gocache go test ./internal/web. Ran git diff --check. All passed.

## Work summary
Fixed phase-row progress metadata so all-done phases whose visible ticket slice is empty still render a full done progress bar when hydrated phase counts report zero active tickets and a nonzero total. Added a regression covering an all-done archived phase hidden by default.

## Learnings
Phase row counts and progress bars can come from different sources: hydrated Phase.TicketCount includes archived tickets, while ListTickets hides them by default. If a row shows 0 active / N total, use those phase counts as the fallback progress source rather than rendering an empty bar.
