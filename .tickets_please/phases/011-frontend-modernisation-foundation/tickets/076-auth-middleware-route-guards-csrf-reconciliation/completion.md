## Testing evidence
go test ./... exits 0 (full suite); go build + GOOS=windows build clean; new internal/web/authmw_test.go covers the role matrix. Detail in follow-up comment.

## Work summary
Web auth middleware + per-project role guards + CSRF reconciliation; committed to main as a1b9f30 (tickets-please/076). Detail in follow-up comment.

## Learnings
make check does NOT run go test (build only); run go test directly. Full W2-3 learnings in the follow-up comment.
