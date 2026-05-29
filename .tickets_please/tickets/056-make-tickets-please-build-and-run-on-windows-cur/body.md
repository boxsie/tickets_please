## Goal

`tickets_please` currently builds and runs on Linux/Unix only. Make the single
binary compile and run correctly on Windows so the file-store + MCP server work
there too.

## Hard blocker (won't even compile on Windows)

- `internal/store/lock.go` imports `golang.org/x/sys/unix` and uses Unix
  `flock`-style locking directly (`LOCK_EX`, etc.). That package is GOOS-gated
  to non-Windows, so `GOOS=windows go build ./...` fails outright. Per-project
  and global locks (`<data_dir>/.lock`, `projects/<slug>/.lock`) are the
  concurrency-safety mechanism, so this can't just be stubbed out.
  - Fix: split file-locking behind a small interface with build-tagged
    implementations — `lock_unix.go` (`//go:build !windows`, current flock) and
    `lock_windows.go` (`//go:build windows`, `LockFileEx` via
    `golang.org/x/sys/windows`, or a cross-platform lib like
    `github.com/gofrs/flock`). `internal/store/agents.go` also touches the
    syscall path — sweep it too.

## Likely-OK but must verify

- **Path handling**: code already uses `path/filepath` (`filepath.Join`,
  `filepath.Separator`) in the store layer — good. Audit the spots flagged by
  `grep` for hardcoded `"/"` splitting/joining (`internal/store/stage.go`,
  `internal/svc/projects.go`, `internal/web/handlers_fs.go`) to ensure none
  assume `/` for *filesystem* paths (URL paths are fine to keep `/`).
- **fsnotify** (`internal/store/watch.go`, `internal/cache/projectcache.go`):
  fsnotify supports Windows (ReadDirectoryChangesW), so the watch path should
  port; verify behavior + the mtime-polling fallback (`fsnotify_enabled: false`)
  works as the Windows safety net.
- **Git auto-commit** (`internal/store/git.go`): confirm it shells out to a
  `git` on PATH (portable if git is installed) vs. anything unix-specific;
  verify `git -C <repo>` works with Windows paths.
- **File modes** (`0o644`/`0o755`, atomic tmp+rename in `vecindex/persist.go`,
  `internal/store/stage.go`): Unix perms are mostly ignored on Windows but
  shouldn't error; `os.Rename` over an existing file differs on Windows
  (may fail if the target is open) — check the atomic-write rename paths.
- **`~` / home-dir + data-root defaults** (`internal/config/config.go`): ensure
  `~`/`%USERPROFILE%` expansion and the `./.tickets_please` default resolve on
  Windows.

## Acceptance

- `GOOS=windows GOARCH=amd64 go build ./...` succeeds from a Unix host.
- `go test ./...` passes on Windows (CI runner or local), including the
  concurrency/lock tests with a real Windows lock implementation.
- A smoke run on Windows: `tickets_please mcp` (stdio) and `tickets_please serve`
  both start; create_project → create_ticket → move → complete round-trips;
  two concurrent processes contend on the lock correctly (no corruption).
- The locking interface is build-tagged so the Unix path is unchanged and the
  Windows path is exercised by the same tests.

## Out of scope

- Windows service/installer packaging.
- Path-length (MAX_PATH) hardening beyond what the default store layout needs.

## Notes

- Start by getting a clean `GOOS=windows go build` (the flock import is the
  single compile blocker); everything else is runtime-correctness to verify
  under test. Consider adding a Windows job to CI so this doesn't regress.
