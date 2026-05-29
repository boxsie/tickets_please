## Testing evidence
Two new tests, both green (full `go test ./...` passes; `-race` on svc clean):

`internal/svc/list_comments_scoped_test.go` (TestListCommentsScoped): seeds a project with two tickets, two agents (A = "me", B = operator), an A user comment, an A column-move (→ system_move), and two B user comments, then asserts:
1. `exclude_author_id=A + exclude_system=true` → exactly B's 2 user comments (the headline "operator feedback" workflow), each carrying ticket_title.
2. `exclude_system=false` → all 4 incl. the system_move.
3. `kinds=[system_move]` → just the move.
4. `ticket_id=t2` scope → only t2's comment.
5. Pagination with `limit=1` walks all 4 distinct comments via `cursor` then goes dry — no dupes.

`internal/mcptools/list_comments_scoped_test.go` (TestHandleListCommentsScoped_Wiring): end-to-end through the handler — default `exclude_system` (true) hides the move and returns just the user note with ticket_title; `exclude_system=false` surfaces 2; a malformed `since` returns a clean argument error (no panic).

`TestRegisterAllTools` updated to expect 31 tools incl. list_comments_scoped (auto-checks count via len(expectedTools)).

## Work summary
Took the recommended new-tool path (existing `list_comments(ticket_id)` untouched).

- `internal/domain/inputs.go`: added `ListCommentsScopedInput` (project/phase/ticket scope; author_id/author_name/exclude_author_id; kinds; exclude_system; since/until; order; limit; cursor) + `time` import.
- `internal/svc/comments.go`: added `ScopedComment{Comment, TicketTitle}` and `Service.ListCommentsScoped` — resolves project via Cache.Get, iterates lp.Tickets (phase/ticket-narrowed via the existing phaseFilterMatches), applies `commentMatchesScopedFilter` (system/kind/author/time), sorts by CreatedAt±ID (asc default, desc via order), and paginates with the same encodeCursor/decodeCursor scheme ListTickets uses (default 50, max 200).
- `internal/mcptools/tools.go`: registered `list_comments_scoped` (12 params) + `handleListCommentsScoped` (parses args, defaults exclude_system=true, RFC3339 since/until, returns {comments:[…ticket_title], next_cursor}).
- Count bumps: `cmd/tickets_please/main.go` totalTools 30→31; tools_test expectedTools Comments 2→3; "30 tools" doc comments → 31.
- Docs: README Comments row 2→3; SPEC Comments section (2)→(3) with a full description row.

## Learnings
- Reused the ListTickets pagination idiom verbatim (encodeCursor/decodeCursor = base64 `<rfc3339nano>|<id>`, "drop up to and including the anchor" slice). It's order-agnostic as long as the SAME deterministic sort is applied before the cursor scan — so asc/desc both work without a second cursor format. Tie-break on ID is essential (comments across different tickets can share a CreatedAt).
- The cache already holds everything needed: `lp.Comments` is `map[ticketID][]*Comment` and `lp.Tickets` is `map[id]*Ticket`, so a project-wide comment list is a nested in-memory walk under lp.Lock.RLock — no store walk, no embeddings. ticket_title comes free from the Ticket in the same map; Comment.TicketID is already populated so I only added TicketTitle to the result.
- exclude_system default-true is a TOOL-layer concern: the Go zero value of a bool is false, so the svc input can't express "unset". The handler sets ExcludeSystem=true then overrides only if `args["exclude_system"].(bool)` is present. Don't try to default it in svc.
- Comment kinds are `user`, `system_move`, `system_completion` (domain.CommentKind*) — the ticket's proposed `kind` values (system/move/completion) didn't match; I exposed a `kinds` array over the real constants and made `exclude_system` mean "kind != user".
- The wider README/SPEC tool tables are independently stale (README still says "30 tools", a phantom `search_projects`, Projects count off) — I only corrected the Comments rows here; the full reconciliation is ticket b26f2ced (docs sweep).
