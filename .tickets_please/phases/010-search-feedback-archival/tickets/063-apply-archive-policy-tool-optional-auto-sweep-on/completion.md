## Testing evidence
go test ./... green; rebuilt + restarted. apply_policy_test.go covers dry-run, commit, refuses-disabled, limit-honored, concurrent. See comment.

## Work summary
ApplyArchivePolicy svc method + apply_archive_policy MCP tool + maybeStartAutoSweep on mount; tools 34 to 35. See comment.

## Learnings
sweepInFlight TryLock pattern coalesces overlapping triggers; cache RLock dropped before flock-taking ArchiveTicket calls. See comment.
