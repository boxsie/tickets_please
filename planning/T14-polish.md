---
id: T14
title: Polish (input validators, pagination, README quickstart)
status: TODO
owner: ""
depends_on: [T07, T12]
parallelizable_with: [T13]
wave: 9
files:
  - internal/svc/validation.go
  - internal/svc/middleware.go
  - internal/svc/tickets.go
  - README.md
estimate: small
stretch: true
---

# T14 — Polish *(stretch)*

## Scope

Tidy the rough edges once the system is end-to-end functional. None of these are required to ship.

**In:** Centralized input validators, cursor pagination cleanup, README quickstart pass.

**Out:** No new features. No protovalidate (we removed protobuf entirely — no .proto files to annotate).

## Files

- `internal/svc/validation.go` — extract per-field validators into reusable helpers
- `internal/svc/tickets.go` — pagination cleanup if T05 left it hand-rolled
- `README.md` — quickstart pass

## Details

### Centralized validators

Pull repeated validation patterns out of handlers into `internal/svc/validation.go`:

```go
func requireNonEmpty(field, val string) error
func requireMinLen(field, val string, min int) error
func requireSlug(field, val string) error
func requireSummary(val string) error  // ≥200 chars after trim
func requireMoveTargetColumn(c domain.Column) error  // rejects empty + done
```

These centralize the messages T05/T06/T07/T16 validate. Refactor handlers to call them. No behavior change.

### Cursor pagination

Sanity-check `ListTickets`:
- Cursor decoding is robust to garbage input (return `domain.ErrInvalidArgument`).
- Ordering predicate is consistent with cursor compare.
- End-of-list returns `next_cursor = ""`.
- Cap honored.

### README quickstart

Reorganize `README.md` around a single 5-step quickstart:

1. `make init-data` — create the data dir scaffold.
2. `make init-config` — drop a sample config in `~/.tickets_please/`.
3. `ollama serve && ollama pull nomic-embed-text` — embedding provider.
4. `make build` — single binary.
5. `claude mcp add tickets_please /abs/path/to/tickets_please mcp` — register with Claude Code (or use the Claude Desktop config snippet).

Cold clone to working LLM workflow in <5 minutes.

## Acceptance criteria

- [ ] All handler-level validation calls go through `internal/svc/validation.go` helpers.
- [ ] Pagination edge cases (empty cursor, garbage cursor, last page) all behave correctly.
- [ ] README quickstart works for someone who hasn't seen the project before.

## Notes

Stretch ticket. Skip if T01–T12 + T13 are already enough. **No protovalidate, no buf** — those refer to a previous architecture.
