## Testing evidence
- `go build ./...` clean.
- `go test ./...` passes (cmd, internal/cache, config, embed, mcptools, store, svc, vecindex, web, worker).
- New tests added:
  - `internal/svc/projects_create_at_test.go::TestCreateProjectAt_ExistingPath_UsedAsIs` — existing local dir is honoured as-is; project.yaml lands inside.
  - `TestCreateProjectAt_MissingPath_NoRoot_Rejected` — when RemoteProjectRoot is empty (strict stdio mode), missing path returns ErrInvalidArgument and the path is not created.
  - `TestCreateProjectAt_MissingPath_UnderRoot_Materialised` — with RemoteProjectRoot set, a missing path under it is mkdir'd and project.yaml is written.
  - `TestCreateProjectAt_MissingPath_OutsideRoot_Rejected` — missing path outside the root returns ErrInvalidArgument with a message containing `remote_project_root`; path stays absent.
  - `TestPathUnderRoot` — table test covering exact-match, child, sibling-prefix, trailing-separator, relative-input, and empty-root cases.
  - `internal/mcptools/register_agent_test.go::TestRegisterAgent_NonExistentPathSurfacesBootstrapHint` — register_agent against a never-existed path now returns the same "no .tickets_please/project.yaml at … call create_project first … no session required" bootstrap message instead of the old stat-precondition error.
- `go vet ./...` clean.

## Work summary
Made `create_project` materialise the directory on the server when `project_path` doesn't exist, bounded by a new config root, and dropped the redundant stat precondition from `register_agent` so missing paths surface the existing bootstrap hint uniformly.

Changes:
- `internal/config/config.go`: added `RemoteProjectRoot` field (koanf `remote_project_root`) with default `~/.tickets_please/projects`; tilde-expanded at load.
- `examples/config.yaml`: documented the new key.
- `cmd/tickets_please/main.go`: added `--remote-project-root` flag to `serve`; logged at startup.
- `internal/svc/projects.go::CreateProjectAt`: replaced the `if stat fails → reject` block with a three-way switch — exists+dir → ok, exists+not-dir → reject, IsNotExist → look up cfg.RemoteProjectRoot, mkdir if `repoPath` is under it, otherwise reject with a message pointing at `--remote-project-root`. Added `pathUnderRoot` helper.
- `internal/mcptools/tools.go::handleRegisterAgent`: removed the unconditional `os.Stat(projectPath)` check. `store.ReadYAML` already returns IsNotExist for missing dirs/files, hitting the existing "call create_project first" branch with the bootstrap escape-valve guidance.
- `internal/mcptools/tools.go` (create_project tool description) and `internal/mcptools/instructions.go` (bootstrap docs): clarified that the server materialises missing paths under `remote_project_root`.
- Tests added: see testing_evidence.

Empty `RemoteProjectRoot` is the kill-switch: it reverts to the strict pre-HTTP "missing path → reject" semantics. Existing-path semantics are unchanged in all modes.

## Learnings
- **The stat precondition lives in two layers, not one.** The ticket pointed at `internal/mcptools/tools.go:1251` for register_agent (correct) but the create_project equivalent isn't in the tools handler — it's in `internal/svc/projects.go::CreateProjectAt` (lines 77–81 pre-fix). The tools-layer create_project handler just calls into svc. Anyone touching `create_project` validation should look in svc, not mcptools, even though tickets routinely cite the mcptools file.
- **`RegisterProjectMount` also stats the path** (`internal/svc/service.go:384`). After `CreateProjectAt` materialises the dir, the subsequent RegisterProjectMount call (both inside createProjectImpl and inside register_agent) succeeds because the dir now exists — but it's a second stat-style check to remember if anyone wants to fully decouple project_path from server-local existence later.
- **`store.ReadYAML` propagates `os.ReadFile`'s `*PathError`, which satisfies `os.IsNotExist` even when the *directory* is missing** (not just the file). That's load-bearing for the register_agent fix — without that, dropping the stat would break the friendly error message. Tested via `TestRegisterAgent_NonExistentPathSurfacesBootstrapHint`.
- **koanf does not tilde-expand**: any new path-shaped config key must be added to the `expandTilde(cfg.X = ...)` sequence in `config.Load`. Missed this on the first pass and the default `~/.tickets_please/projects` would have stayed literal.
- **Defaults sit in two places**: `defaults` map in `internal/config/config.go` AND `examples/config.yaml`. The package doc says "keep them in lockstep" — easy to forget the yaml file.
- **`pathUnderRoot` must require both inputs to be absolute.** A relative `root` makes "under" meaningless and would silently accept paths that happen to share a prefix string. Encoded as a hard `false` to fail loudly rather than degrading to a string-prefix check.
- **Tests use a separate `RemoteProjectRoot`-less freshServiceWithCfg for the legacy strict path** — passing `RemoteProjectRoot: ""` explicitly documents the kill-switch behavior. If you want stdio-strict-everywhere back, that's the one knob.
- **Workspace gopls noise**: this repo has two checkouts (`/mnt/data/projects/tickets_please` and `/home/dan/Documents/projects/tickets_please`). Editing the Documents one trips gopls "not in workspace" diagnostics that are spurious — `go build ./...` from the right cwd is the source of truth.
