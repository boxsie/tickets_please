---
id: T07
title: MoveTicket + CompleteTicket
status: TODO
owner: ""
depends_on: [T05, T06]
parallelizable_with: [T08, T09]
wave: 3
files:
  - internal/svc/validation.go
  - internal/svc/tickets.go
estimate: medium
stretch: false
---

# T07 — MoveTicket + CompleteTicket

## Scope

The **load-bearing rules** of the system, now atop filesystem storage:

1. Every column move requires a comment.
2. `done` is reachable only via `CompleteTicket`, which requires testing evidence, work summary, and learnings.

Both operations are atomic: ticket file update + system comment file insertion via a single `StageOp` (multiple files, single rename phase).

**In:** Two new RPC handlers, shared validators, atomic multi-file writes, system-completion comment formatting.

**Out:** No embedding work — T10 patches the markers in this handler.

## Files

- `internal/svc/validation.go` *(new — shared validators)*
- `internal/svc/tickets.go` *(extend with MoveTicket + CompleteTicket)*

## Details

### `validation.go`

```go
func requireNonEmptyTrimmed(field, val string) error
func requireMinLen(field, val string, min int) error
func requireMoveTargetColumn(c ticketsv1.Column) error  // rejects UNSPECIFIED and DONE
```

DONE rejection message: `target_column DONE is not allowed; use CompleteTicket to mark a ticket done`.

### `MoveTicket`

1. Validate `target_column` and `comment` (non-empty after trim).
2. Lazy-load project; `loaded.Lock.Lock()`.
3. Look up `t := loaded.Tickets[ticket_id]`. `NotFound` if missing.
4. If `t.Column == done` → `FailedPrecondition` (no-reopen rule).
5. **Dependency check** when moving from `todo` → `in_progress`:
   - Compute `blocked_by` against current ticket states.
   - If non-empty AND `cfg.EnforceDependencies` is true → `FailedPrecondition` listing the unmet dep ids in the error message.
   - If non-empty AND enforcement is off → log a warning, append a system note to the move comment body (`⚠ moved with unmet deps: [...]`), and proceed.
6. Build the new ticket state (`t.Column = target_column`, `t.UpdatedAt = now`).
6. Build the `system_move` comment: kind=system_move, body=`comment`, from_column=old, to_column=new, author_id=agent.id.
7. **Single `StageOp`** writing both:
   - `tickets/<dir>/ticket.yaml` (updated)
   - `tickets/<dir>/comments/<filename>` (new)
8. Commit. Caption: `[tickets_please] move <slug>/<NNN> <old> → <new> [<agent>]`.
9. Update in-memory `loaded.Tickets[id]` and append to `loaded.Comments[id]`.
10. Enqueue `JobComment` for the new system comment (T10 marker).
11. Re-read and return the ticket.

### `CompleteTicket`

1. Validate `requireMinLen` ≥10 chars on testing_evidence, work_summary, learnings (each).
2. Lazy-load project; `loaded.Lock.Lock()`.
3. Look up ticket. If already `done` → `FailedPrecondition`.
4. Build new ticket state: `column=done`, `testing_evidence`/`work_summary`/`learnings` populated, `completed_at=now`, `completed_by=agent.id`, `updated_at=now`.
5. Build the `completion.md` content using the canonical format:

   ```markdown
   ## Testing evidence
   <testing_evidence>

   ## Work summary
   <work_summary>

   ## Learnings
   <learnings>
   ```

6. Build the `system_completion` comment whose body is the same content (so `list_comments` shows it inline):

   ```
   ✅ Ticket completed.

   Testing evidence:
   <testing_evidence>

   Work summary:
   <work_summary>

   Learnings:
   <learnings>
   ```

7. **Single `StageOp`** writing:
   - `tickets/<dir>/ticket.yaml` (updated)
   - `tickets/<dir>/completion.md` (new)
   - `tickets/<dir>/comments/<filename>` for the system_completion comment (new)
8. Commit. Caption: `[tickets_please] complete <slug>/<NNN> [<agent>]`.
9. Update in-memory state.
10. Enqueue `JobTicketLearnings` (writes `learnings.embedding.json`, updates resident `learnings_index`) **and** `JobComment` for the system_completion comment.
11. Re-read and return.

### Atomicity guarantee

Because every multi-file mutation goes through a single `StageOp`, the rename phase is the failure boundary. An interrupted rename phase leaves a partial state that the next startup's integrity check detects and surfaces.

## Acceptance criteria

- [ ] `MoveTicket` with empty `comment` → `domain.ErrInvalidArgument`, message names the field.
- [ ] `MoveTicket` with `target = ColumnDone` → `domain.ErrInvalidArgument` and the message points at `CompleteTicket`.
- [ ] `MoveTicket` against a `done` ticket → `domain.ErrFailedPrecondition`.
- [ ] Happy-path move: `ticket.yaml` shows new column; a new `system_move` comment file appears with `from_column` and `to_column` set; `ListComments` shows it.
- [ ] `CompleteTicket` with `learnings = "."` → `domain.ErrInvalidArgument`.
- [ ] `CompleteTicket` against an already-done ticket → `domain.ErrFailedPrecondition`.
- [ ] Happy-path complete: `ticket.yaml.column = done`, `completion.md` exists with all three sections, `system_completion` comment file exists, `completed_by` populated from session.
- [ ] Auto-commit: each Move and Complete produces one git commit (not multiple) authored as the calling agent.
- [ ] Atomicity: forcibly killing the process between `StageOp.Write` and `Commit` leaves the ticket unchanged on disk; integrity check at next startup logs the leftover staging dir.

## Notes

See **Validation & enforcement**, **Data layout**, and **Atomicity (the staging + rename pattern)** in [`../SPEC.md`](../SPEC.md). T10 will patch `// T10: enqueue embed job here` markers around the StageOp.Commit calls — keep them.
