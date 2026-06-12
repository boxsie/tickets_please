## Testing evidence
Ran make css. Ran GOCACHE=/tmp/tickets-please-gocache go test ./internal/web -run 'TestStatic|TestPhasesIndex_EnrichesWithWaves'. Ran GOCACHE=/tmp/tickets-please-gocache go test ./internal/web. Ran git diff --check. Fetched /static/app.css from the local dev server and verified the phase-wave-ticket-meta grid selectors are present.

## Work summary
Changed phase wave ticket-row metadata from a content-sized flex wrapper to fixed grid columns: status badge, optional archived pill, and a truncating attribution column. Regenerated the compiled CSS so the embedded stylesheet carries the fix.

## Learnings
Phase wave ticket rows need the whole trailing metadata wrapper to have a stable width; fixing only the badge min-width is not enough because the row grid auto column still expands for long attribution text. Use fixed subcolumns and truncate the author inside the attribution cell.
