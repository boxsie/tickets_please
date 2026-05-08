## Goal

Stop committing `*.embedding.json` files. Cold-clone path should rebuild from scratch via the existing missing-sidecar enqueue.

## Scope

- `cmd/tickets_please/main.go:266` (`runInit`) — write `<dataDir>/.gitignore` containing:
  ```
  *.embedding.json
  .staging/
  ```
  Skip if the file already exists (don't clobber user edits).
- One-time write of that same `.gitignore` into `/home/dan/Documents/projects/tickets_please/.tickets_please/.gitignore` and `/mnt/data/projects/tickets_please/.tickets_please/.gitignore` (whichever clones the user actually runs against — both have the data dir).
- Run `git rm --cached '*.embedding.json'` once (with user confirmation) inside the repo to evict already-tracked sidecars. Commit the eviction + the .gitignore in a single change.

## Tests

- New unit in `cmd/tickets_please/init_test.go` (or wherever runInit is tested): runInit on a tempdir leaves `<dir>/.gitignore` with both lines; running it twice doesn't clobber an edited gitignore.

## Done when

- `git status` after a re-embed shows zero `*.embedding.json` churn.
- A fresh `git clone` of this repo has zero sidecars on disk; `tickets_please serve` against it logs N enqueued embed jobs and rebuilds them automatically.
