## Phase: Bootstrap UX

### The bug

A fresh HTTP MCP session in a repo without `.tickets_please/project.yaml` is structurally locked out — and the error messages don't say so. Real transcript captured 2026-05-04:

```
who_am_i             → registered=false (expected; HTTP needs register_agent)
create_project       → "unauthenticated: no agent registered ... call register_agent first"
register_agent       → "no .tickets_please/project.yaml at /path"
register_agent  (re) → same error
list_projects        → unauthenticated
```

The agent then spent ten more tool calls spelunking `~/.tickets_please/`, the central registry, the agents directory, looking for a CLI binary, etc. — because the two errors form a paradox with no documented escape:

- `create_project` says "register first" (`internal/mcptools/tools.go:314`)
- `register_agent` says "no project.yaml" (`internal/mcptools/tools.go:1242`)
- `project.yaml` only exists *after* `create_project` succeeds.

The escape today is "use a stdio session — it pre-registers at startup with no project binding, so create_project works." That fact is documented nowhere the agent looks.

### Why fix it

Auto-memory feedback record (`feedback_dogfood_tickets.md`) says non-trivial work in this repo gets ticketed in tickets_please first. Agents that can't onboard new repos can't do that. Every fresh repo currently requires either a human walking the agent through the bootstrap flow or the agent stumbling into spelunking — both bad. Three cheap changes turn the failure mode into a self-explanatory diagnostic.

### Scope: three leverage points

1. **Server error messages** (`internal/mcptools/tools.go:314, 1242`). Both errors should name the cold-start flow explicitly: from a populated repo, `register_agent`; from an empty repo, `create_project` from a stdio session (pre-registered) — then come back to `register_agent` for HTTP follow-on. The errors are an LLM's primary information channel; right now they hand it a paradox.
2. **`ServerInstructions`** (`internal/mcptools/instructions.go:9`). The "Workflow reflexes" section documents the steady-state flow but says nothing about cold-start. Add a short "Bootstrapping a new project" section so the cold-start path is in the LLM's context every turn, not just when an error fires.
3. **`dataDirReadme`** (`cmd/tickets_please/main.go:560`). The per-repo README written by `tickets_please init` describes the on-disk layout but not the cold-start flow. Add a "How to bootstrap from here" section. Lowest leverage of the three (READMEs lose to live error messages in observed agent behavior), but cheap.

### Out of scope

- Architecturally fixing the chicken-and-egg so HTTP can cold-start without a stdio bootstrap (would need `register_agent` to support a no-project-bound mode, or `create_project` to self-register). Worthwhile follow-up phase, but bigger than the current friction warrants.
- Changing the stdio pre-registration flow.
- Any web-UI work.

### Critical files

- `internal/mcptools/tools.go` (lines 297-327 callWithRetry; 1206-1300 handleRegisterAgent)
- `internal/mcptools/instructions.go` (the ServerInstructions const)
- `cmd/tickets_please/main.go` (the dataDirReadme const, line 560-575)
- `internal/mcptools/register_agent_test.go:131-141` — existing test for the "no project.yaml" message; likely needs message-text update.

### Verification

- `go test ./internal/mcptools/...` green (with updated assertion text).
- Manual check: stdio launch in a fresh tempdir, follow only the error-message guidance — bootstrap completes without consulting external docs.
