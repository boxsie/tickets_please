---
id: T01
title: Bootstrap module, Makefile, single binary, data dir
status: TODO
owner: ""
depends_on: []
parallelizable_with: []
wave: 0
files:
  - go.mod
  - Makefile
  - internal/config/config.go
  - examples/config.yaml
  - .tickets_please/README.md
  - cmd/tickets_please/main.go
  - README.md
  - .gitignore
estimate: small
stretch: false
---

# T01 — Bootstrap module, Makefile, single binary, data dir

> Must complete before any other ticket starts.

## Scope

Stand up the empty skeleton so every other ticket has a place to land its code. Filesystem-backed (no database), no docker, no migrations.

**In:** Go module init, Makefile targets, koanf config loader (file + env), `.tickets_please/` data dir scaffold, ONE compilable-but-stub binary with subcommand dispatch, README skeleton.

**Out:** No protobuf, no buf, no second binary, no storage code, no business logic. Don't write anything that belongs in T02+.

## Files

- `go.mod` (`module tickets_please`)
- `Makefile`
- `internal/config/config.go`
- `examples/config.yaml` *(already shipped — keep in lockstep with the defaults map)*
- `.tickets_please/README.md` *(short orientation file inside the data dir; explains the layout to anyone browsing the repo)*
- `cmd/tickets_please/main.go` *(single binary; subcommand dispatcher; subcommand bodies are stubs that T12 fills in)*
- `README.md`
- `.gitignore` (Go + `.tickets_please/.staging/`; **must not** ignore `examples/config.yaml` or the rest of `.tickets_please/`)

## Details

- Module name is exactly `tickets_please`.
- `Makefile` targets:
  - `build` — `go build -o tickets_please ./cmd/tickets_please`
  - `run` — `go run ./cmd/tickets_please mcp` (default subcommand stub for now)
  - `test` — `go test ./...`
  - `init-config` — `mkdir -p ~/.tickets_please && cp -n examples/config.yaml ~/.tickets_please/config.yaml`
  - `init-data` — `mkdir -p .tickets_please/{agents,projects,.staging}` (idempotent)
  - `check` — `go run ./cmd/tickets_please check`
- **No `buf.yaml` / `buf.gen.yaml`. No `proto/` directory. No `gen/` directory.** The protobuf layer was removed in favor of an in-process Go API (see [`../SPEC.md`](../SPEC.md) §Architecture).

### `internal/config/config.go`

Use `github.com/knadh/koanf/v2` with three providers, layered in order:

1. **Defaults** — `map[string]any` literal in code mirroring `examples/config.yaml`.
2. **YAML file** — `~/.tickets_please/config.yaml` if it exists. Resolve `~` via `os.UserHomeDir()`. Missing file is **not** an error.
3. **Environment** — koanf's `env` provider with prefix `""`, delimiter `_`. Uppercase keys (`data_dir` ← `DATA_DIR`).

```go
type Config struct {
    DataDir                string `koanf:"data_dir"`
    AutoCommit             bool   `koanf:"auto_commit"`
    EmbedProvider          string `koanf:"embed_provider"`
    OllamaURL              string `koanf:"ollama_url"`
    OllamaModel            string `koanf:"ollama_model"`
    OpenAIKey              string `koanf:"openai_api_key"`
    MCPAgentKey            string `koanf:"mcp_agent_key"`
    MCPAgentName           string `koanf:"mcp_agent_name"`
    AgentSessionTTLMinutes int    `koanf:"agent_session_ttl_minutes"`
    AgentSessionMaxMinutes int    `koanf:"agent_session_max_minutes"`
    ProjectIdleMinutes     int    `koanf:"project_idle_minutes"`
    MaxLoadedProjects      int    `koanf:"max_loaded_projects"`
    LockTimeoutSeconds     int    `koanf:"lock_timeout_seconds"`
    FsnotifyEnabled        bool   `koanf:"fsnotify_enabled"`
    EnforceDependencies    bool   `koanf:"enforce_dependencies"`
}

func Load() (Config, error) { /* layered koanf load */ }
```

Defaults map:

```go
var defaults = map[string]any{
    "data_dir":                  "./.tickets_please",
    "auto_commit":               true,
    "embed_provider":            "ollama",
    "ollama_url":                "http://localhost:11434",
    "ollama_model":              "nomic-embed-text",
    "openai_api_key":            "",
    "mcp_agent_key":             "",  // empty = generated at startup
    "mcp_agent_name":            "tickets_please_mcp",
    "agent_session_ttl_minutes": 60,
    "agent_session_max_minutes": 240,
    "project_idle_minutes":      15,
    "max_loaded_projects":       16,
    "lock_timeout_seconds":      10,
    "fsnotify_enabled":          true,
    "enforce_dependencies":      false,
}
```

On first successful load, log at info: `using config from <path>` (or `using defaults; create ~/.tickets_please/config.yaml or run \"make init-config\" to customize`).

### `cmd/tickets_please/main.go`

Single binary, subcommand dispatch. Subcommands:

- `mcp` *(default — what runs when no subcommand given)* — stub: log "mcp mode not implemented yet, see T12" and exit 0.
- `check` — stub: log "integrity check not implemented yet, see T02" and exit 0.
- `init` — `mkdir -p` the data dir scaffold (`.tickets_please/{agents,projects,.staging}`) and write the `.tickets_please/README.md` if missing.

All subcommands load config, build a slog JSON logger, log the loaded config source, then run their stub. T02–T15 fill in the bodies.

### `.tickets_please/README.md` content

Briefly explain what's in the data dir for anyone clicking around the repo:
- `agents/<uuid>.yaml` — agent session records.
- `projects/<slug>/` — one dir per project, containing `project.yaml`, `summary.md`, embedding sidecars, and `tickets/`.
- `.staging/` — transient atomicity scratch dir; safe to delete when the server isn't running.
- Pointer to `../SPEC.md` for the canonical layout.

### Top-level README skeleton

A short README at `README.md` with sections:
- One-paragraph intro pointing at `SPEC.md`.
- Quickstart: `make init-config`, `make init-data`, `ollama pull nomic-embed-text`, `make build`, register the binary with your MCP-capable client.
- Pointer to `planning/` for in-flight work.
- Note that `.tickets_please/` is committed and IS the data — pull/clone brings ticket history with you.

Don't reference any absolute paths or personal directories. The repo is intended to be public.

## Acceptance criteria

- [ ] `go mod tidy` succeeds with no diagnostics.
- [ ] `make build` produces a single `tickets_please` binary.
- [ ] `make init-config` creates `~/.tickets_please/config.yaml` from `examples/config.yaml`. Re-running it does not overwrite.
- [ ] `make init-data` creates `.tickets_please/{agents,projects,.staging}` and is idempotent.
- [ ] `./tickets_please` (no args) runs the `mcp` subcommand stub, logs the loaded config source, exits cleanly.
- [ ] `./tickets_please check` and `./tickets_please init` both run their stubs cleanly.
- [ ] `DATA_DIR=/tmp/foo ./tickets_please init` creates the scaffold under `/tmp/foo` (env-var override works).
- [ ] Removing `~/.tickets_please/config.yaml` and rerunning falls back to defaults without erroring.
- [ ] `.gitignore` does NOT ignore `.tickets_please/` (only `.tickets_please/.staging/`).

## Notes

See **Project layout**, **Tech stack**, **Configuration**, and **Project loading & in-memory cache** in [`../SPEC.md`](../SPEC.md). Keep `examples/config.yaml`, the defaults map, and the Configuration table in lockstep.

**No docker. No sqlite.** If you find yourself reaching for either, stop — the spec moved to filesystem storage.
