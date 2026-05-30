## Testing evidence
go test ./... green; rebuilt + restarted service. New tests in svc/feedback_retrieval_test.go and mcptools/feedback_hint_test.go.

## Work summary
EntryKey on hits; recordSearchRetrievals per-slug; feedback_hint only on non-empty. Detail in ticket comment.

## Learnings
IIFE for lock-then-flock; per-slug map for cross-mount aggregates. Detail in ticket comment.
