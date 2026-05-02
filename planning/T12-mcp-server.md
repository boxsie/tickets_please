---
id: T12
title: MCP binary entry point
status: TODO
owner: ""
depends_on: [T03, T04, T05, T06, T07, T11, T15, T16]
parallelizable_with: []
wave: 5
files:
  - cmd/tickets_please/main.go
  - internal/mcptools/tools.go
  - internal/mcptools/format.go
  - internal/mcptools/identity.go
  - README.md
estimate: medium
stretch: false
---

# T12 — MCP binary entry point

## Scope

The single binary that runs the whole show. Subcommand-dispatched: `mcp` (default) is the stdio MCP server. All logic in-process — no separate gRPC service.

**In:** `cmd/tickets_please/main.go` with subcommand dispatch, `internal/mcptools/` package wrapping `svc.Service` methods as MCP tools, MCP-side agent self-registration with auto-re-register on session expiry, README "Wiring up MCP" section.

**Out:** No `serve` mode (future). No HTTP/gRPC. No multi-process coordination beyond what T02 already provides (flock + fsnotify).

## Files

- `cmd/tickets_please/main.go` — CLI dispatch
- `internal/mcptools/tools.go` — tool registration, one handler per tool
- `internal/mcptools/format.go` — `domain.*` → LLM-friendly JSON (snake_case keys, columns as strings)
- `internal/mcptools/identity.go` — MCP self-registration + session caching
- `README.md` — "Wiring up MCP" section

## Details

### CLI dispatch

```go
func main() {
    cfg := config.MustLoad()
    log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

    sub := "mcp"
    if len(os.Args) > 1 { sub = os.Args[1] }

    switch sub {
    case "mcp":   runMCP(cfg, log)
    case "check": runCheck(cfg, log)
    case "init":  runInit(cfg, log)
    case "help", "-h", "--help": printUsage()
    default:
        fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", sub)
        os.Exit(2)
    }
}
```

`runMCP`:
1. Build `svc.Service` via `svc.New(cfg)` — this opens the Store, builds the project cache, starts the embedding worker.
2. Create the MCP server via `mark3labs/mcp-go`.
3. Self-register as an agent (see Identity below).
4. Register all tools.
5. `server.ServeStdio(srv)` — blocks until stdin closes.
6. On exit, drain the worker, release locks, close fsnotify watchers.

`runCheck` calls `store.Integrity` and prints results; exits 0 on success, non-zero on structural failure.
`runInit` creates `.tickets_please/{agents,projects,.staging}` and writes a starter `.tickets_please/README.md`.

### Identity (`internal/mcptools/identity.go`)

```go
type Identity struct {
    Key       string
    Name      string
    SessionID string
    ExpiresAt time.Time
}

func NewIdentity(cfg config.Config) Identity
func (id *Identity) Register(ctx context.Context, svc *svc.Service) error
func (id *Identity) AttachContext(ctx context.Context) context.Context
// AttachContext sets the session-id key the svc middleware reads.
```

Defaults:
- `Key` = `cfg.MCPAgentKey` if set, else `tickets_please_mcp:<random-8-hex>`.
- `Name` = `cfg.MCPAgentName` if set, else `tickets_please_mcp`.

### Tool registration (`internal/mcptools/tools.go`)

One handler per tool from the spec's **MCP server** table. Use `mcp.NewTool(name, mcp.WithDescription(...), mcp.WithString("param", ...))` style. Tool descriptions are **canonical** in [`../SPEC.md`](../SPEC.md) — copy verbatim, do not rephrase.

Handler shape:

```go
func (t *Tools) handleCreateTicket(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
    ctx = t.identity.AttachContext(ctx)
    in := domain.CreateTicketInput{ /* parse args */ }
    ticket, err := t.svc.CreateTicket(ctx, in)
    if err != nil {
        if errors.Is(err, domain.ErrUnauthenticated) {
            // re-register and retry once
            if rerr := t.identity.Register(ctx, t.svc); rerr == nil {
                ctx = t.identity.AttachContext(ctx)
                ticket, err = t.svc.CreateTicket(ctx, in)
            }
        }
        if err != nil { return mcp.ErrorResult(formatError(err)), nil }
    }
    return mcp.JSONResult(format.Ticket(ticket)), nil
}
```

Error mapping (`format.go`):

| `domain.ErrXXX` | MCP error message prefix |
|---|---|
| `ErrInvalidArgument` | `invalid argument: <message>` |
| `ErrNotFound` | `not found: <id>` |
| `ErrAlreadyExists` | `already exists: <slug-or-id>` |
| `ErrFailedPrecondition` | `precondition failed: <message>` |
| `ErrUnauthenticated` | `unauthenticated; re-registering...` (caller retries) |

### Tool list

All from the spec's **MCP server** section, plus `who_am_i`:

`list_projects`, `create_project`, `get_project_summary`, `load_project`, `update_project`, `delete_project`, `create_phase`, `list_phases`, `get_phase_summary`, `update_phase`, `assign_ticket_to_phase`, `create_ticket`, `get_ticket`, `list_tickets`, `update_ticket`, `move_ticket`, `complete_ticket`, `add_comment`, `list_comments`, `search_projects`, `search_tickets`, `search_learnings`, `search_comments`, `who_am_i`.

`who_am_i` is a special tool that doesn't call svc — it returns `t.identity` from process state.

### Output formatting

Convert `domain.*` structs to plain maps with snake_case keys, columns as strings, agent refs as `{"id":"…","name":"…"}`. Keep IDs as strings, timestamps as RFC3339.

### README "Wiring up MCP"

```bash
# Build
go build -o tickets_please ./cmd/tickets_please

# Initialize a data dir + sample config
./tickets_please init
make init-config

# Pull an embedding model
ollama pull nomic-embed-text

# Wire to Claude Code
claude mcp add tickets_please /abs/path/to/tickets_please mcp
```

For Claude Desktop, document the config snippet:
```json
{
  "mcpServers": {
    "tickets_please": {
      "command": "/abs/path/to/tickets_please",
      "args": ["mcp"],
      "env": {
        "MCP_AGENT_NAME": "Claude Desktop"
      }
    }
  }
}
```

Include a "First run" recipe: ask Claude to "create a project called 'demo' with a thoughtful summary, then a ticket, then move it to in_progress with a reason."

## Acceptance criteria

- [ ] `make build` produces a single binary.
- [ ] `./tickets_please mcp` boots, self-registers as an agent (creates `.tickets_please/agents/<uuid>.yaml`), then waits for stdio input.
- [ ] `./tickets_please check` runs the integrity walk and exits cleanly.
- [ ] `./tickets_please init` creates the data dir scaffold idempotently.
- [ ] LLM end-to-end (in a Claude Code chat with the MCP registered):
  - All 24 tools listed.
  - `create_project` rejects when summary < 200 chars.
  - `create_ticket` works.
  - `move_ticket` rejects without a comment, succeeds with one.
  - `complete_ticket` rejects with thin learnings, succeeds with substantive ones.
  - `search_learnings` finds a paraphrased query.
  - `who_am_i` returns the MCP's identity.
  - `assign_ticket_to_phase` requires a comment and physically moves the ticket directory.
- [ ] Forcing the agent session to expire mid-conversation (via `AGENT_SESSION_TTL_MINUTES=1` or wall-clock test): the next tool call transparently re-registers and succeeds.

## Notes

See **Architecture: one binary, MCP-first**, **MCP server**, and **Agent identity & sessions > MCP integration** in [`../SPEC.md`](../SPEC.md). Don't shorten the load-bearing tool descriptions — they're the unlock that makes LLMs use `search_learnings` and `get_project_summary` proactively.
