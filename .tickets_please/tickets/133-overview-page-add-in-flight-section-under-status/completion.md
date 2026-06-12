## Testing evidence
Ran env GOCACHE=/tmp/tickets-please-gocache go test ./internal/web -run 'TestProjects_Detail|TestProjects_Detail_InFlightSection' and env GOCACHE=/tmp/tickets-please-gocache go test ./internal/web; both passed. Also verified the rebuilt local server rendered the In flight section on /p/tickets-please.

## Work summary
Added the project overview In flight card under Status distribution, populated it from in_progress and testing tickets, rendered links/badges/attribution, regenerated templ output, and covered the section with a web handler regression test.

## Learnings
Project overview dashboard data is derived from a single ListTickets call in handlers_projects.go; new dashboard sections should extend projectDetailData and DetailProps together, then regenerate templ output with the installed templ binary.
