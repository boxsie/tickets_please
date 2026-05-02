## Testing evidence
Ran npm --prefix frontend run typecheck, npm --prefix frontend run test -- --run, npm --prefix frontend run build, go test ./..., and make build successfully. The App test verifies project, phase, wave, and ticket data load from the API and that selected project/phase/wave state is written to the URL. Live API smoke verified the wave endpoint returns waves 1 through 5 from the dogfood project.

## Work summary
Built the initial app navigation shell: project selector, phase tabs, wave selector, URL-backed selected state, TanStack Query API reads, status counters, ticket list preview, loading and error surfaces, and search input placeholder for the later search ticket.

## Learnings
URL query state is enough for first-pass project, phase, and wave persistence without introducing a router yet. The navigation shell should stay independent of the eventual board layout so the board ticket can focus on columns and drag behavior.
