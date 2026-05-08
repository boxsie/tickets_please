## Testing evidence
`make build` clean; `go test ./...` green including new `cmd/tickets_please/init_test.go` (`TestRunInitWritesGitignore`, `TestRunInitDoesNotClobberEditedGitignore`). Eviction confirmed: `git ls-files | grep embedding.json` returns empty after the merge. `.tickets_please/.gitignore` exists in the repo root data dir with the two expected lines. The single previously-tracked sidecar (`.tickets_please/summary.embedding.json`) was removed via `git rm --cached` in the same commit.

## Work summary
runInit (`cmd/tickets_please/main.go`) now skip-if-exists writes `<dataDir>/.gitignore` containing `*.embedding.json` + `.staging/`, mirroring the existing README handling pattern (`os.Stat` first, write only on `ErrNotExist`). Added a `dataDirGitignore` const next to the existing `dataDirReadme`. Created `.tickets_please/.gitignore` in this repo's data dir directly, and ran `git rm --cached '*.embedding.json'` to evict the one tracked sidecar (`summary.embedding.json`). Tests cover both the "writes when missing" and "doesn't clobber when present" cases.

## Learnings
Pattern lifted directly from the existing `dataDirReadme` handling — when adding a new data-dir bootstrap file, the codebase already has the convention (skip-if-exists, file mode 0644, const at the bottom of main.go). Don't reinvent. Also: `git rm --cached '*.embedding.json'` is idempotent — if the worktree has zero matching tracked files, it just errors and that's fine to ignore. We had exactly one tracked sidecar in this repo's history, so the eviction commit is small and audit-friendly.
