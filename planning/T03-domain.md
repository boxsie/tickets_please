---
id: T03
title: Domain types & MCP tool schemas
status: DONE
owner: subagent-T03
depends_on: [T01]
parallelizable_with: [T02, T08, T15]
wave: 1
files:
  - internal/domain/types.go
  - internal/domain/errors.go
  - internal/domain/inputs.go
estimate: small
stretch: false
---

# T03 — Domain types & MCP tool schemas

## Scope

Hand-write the Go **hydrated** domain types every other ticket needs. These are what `svc.Service` methods return — full Project, Phase, Ticket, Comment, including the fields that live in sibling markdown files (`Summary`, `Body`, `Learnings`).

**Disk record types are NOT this ticket's job.** Those (`store.ProjectRecord`, `store.TicketRecord`, etc.) live in T02. The cache layer assembles records + sibling files into the hydrated `domain.*` types defined here.

**In:** `internal/domain/` package: hydrated structs for Project, Phase, Ticket, Comment, Agent; enum-style typed strings; error sentinels; input structs that `svc.Service` methods accept.

**Out:** No proto files, no buf, no codegen. No `svc.Service` interface (T15 owns that). No yaml-tag round-tripping — domain types don't need to round-trip to disk; that's the record types' job.

## Files

- `internal/domain/types.go` — entity structs + enum types (Column, CommentKind)
- `internal/domain/inputs.go` — the input structs (CreateTicketInput, ListTicketsInput, etc.)
- `internal/domain/errors.go` — `ErrNotFound`, `ErrAlreadyExists`, `ErrInvalidArgument`, `ErrFailedPrecondition`, `ErrUnauthenticated`. Each is a sentinel `error` value. Helpers `IsNotFound(err) bool`, etc.

## Details

### Entity structs

Per the **Service API > Domain types** section of [`../SPEC.md`](../SPEC.md):

```go
type Column     string
type CommentKind string

const (
    ColumnTodo       Column = "todo"
    ColumnInProgress Column = "in_progress"
    ColumnTesting    Column = "testing"
    ColumnDone       Column = "done"
)

const (
    CommentKindUser              CommentKind = "user"
    CommentKindSystemMove        CommentKind = "system_move"
    CommentKindSystemCompletion  CommentKind = "system_completion"
)

type AgentRef struct {
    ID   string
    Name string
}

type Agent struct {
    ID           string
    Key          string
    Name         string
    Metadata     map[string]string
    CreatedAt    time.Time
    ExpiresAt    time.Time
    LastSeenAt   time.Time
}

type Project struct {
    ID, Slug, Name, Description, Summary string
    CreatedBy *AgentRef
    CreatedAt, UpdatedAt time.Time
}

type Phase struct {
    ID, ProjectID, Slug, Name, Description, Summary string
    Number int
    CreatedBy *AgentRef
    CreatedAt, UpdatedAt time.Time
    TicketCount, ActiveTicketCount int  // computed
}

type Ticket struct {
    ID, ProjectID, Title, Body string
    Column           Column
    TestingEvidence, WorkSummary, Learnings *string
    CompletedAt      *time.Time
    CreatedBy, CompletedBy *AgentRef
    CreatedAt, UpdatedAt time.Time
    DependsOn          []string
    ParallelizableWith []string
    BlockedBy          []string  // computed at read
    PhaseID            *string
    Wave               int       // 0 = unassigned; soft grouping inside its phase or project
}

type WaveSummary struct {
    Wave              int
    TicketCount       int
    ActiveTicketCount int   // not done
}

type Comment struct {
    ID, TicketID string
    Kind         CommentKind
    Body         string
    FromColumn, ToColumn *Column
    Author       *AgentRef
    CreatedAt    time.Time
}
```

**No YAML tags on domain types.** YAML round-tripping is the disk record types' responsibility (T02 owns those). Domain types are pure in-memory shapes returned by `svc.Service`.

### Input structs

Defined in `internal/domain/inputs.go`. Each `svc.Service` method takes one of these (or simple positional args for trivial calls).

```go
type CreateTicketInput struct {
    ProjectIDOrSlug    string
    Title              string
    Body               string
    DependsOn          []string
    ParallelizableWith []string
    PhaseIDOrSlug      *string  // nil = no phase
    Wave               int      // 0 = unassigned
}

type UpdateTicketInput struct {
    Title *string
    Body  *string
    Wave  *int   // nil = leave unchanged; *int(0) = set to unassigned
    // No column field — that's what MoveTicket / CompleteTicket are for.
    // No phase field — that's AssignTicketToPhase.
}

type ListTicketsInput struct {
    ProjectIDOrSlug string
    Column          *Column   // nil = any column
    PhaseIDOrSlug   *string   // nil = any phase or none; *"-" (sentinel) = phase-less only; *"foo" = that phase
    Wave            *int      // nil = any wave; *int(N) = exactly that wave; *int(0) = unassigned only
    ReadyOnly       bool
    Limit           int       // 0 = default 50; capped at 200
    Cursor          string
}

type SearchTicketsInput struct {
    Query           string
    ProjectIDOrSlug string  // required in v1
    Columns         []Column
    Limit           int     // 0 = default 10; capped at 50
}

// Similar shapes for SearchCommentsInput, SearchLearningsInput, UpdateProjectInput, UpdatePhaseInput, etc.
```

### MCP tool schemas

`mark3labs/mcp-go` lets you declare typed parameters when registering a tool. Sketch the parameter shapes here so T12 (the MCP layer) can register them. Each MCP tool corresponds to one `svc.Service` method; the tool description (verbatim from the **MCP server** section of the spec) and the parameter list belong together.

This ticket can ship a `tools.txt` or a Go file `internal/domain/tool_schemas.go` listing every `(tool_name, description, []param_schema)` triple as authoritative metadata. T12 imports it.

## Acceptance criteria

- [ ] `internal/domain/types.go` compiles and has all entity structs.
- [ ] `internal/domain/errors.go` compiles; `errors.Is(someErr, domain.ErrNotFound)` works for sentinel values.
- [ ] Input structs cover every `svc.Service` method that other tickets reference.
- [ ] Tool schemas table covers all **28 tools** listed in the **MCP server** section of [`../SPEC.md`](../SPEC.md): 7 project tools, 7 phase tools, 7 ticket tools, 2 comment tools, 4 search tools, 1 introspection tool.

## Notes

See **Service API**, **Domain types**, **MCP server** in [`../SPEC.md`](../SPEC.md). No protobuf. If you find yourself reaching for `buf` or `protoc`, stop — the spec moved past that.

T15 reuses `Agent`, `AgentRef`. T04+ reuse the entity + input structs. T12 reuses the tool schemas.
