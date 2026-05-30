## Problem

`complete_ticket` currently requires three fields, each ≥10 chars:

- `testing_evidence` — what you tested and how
- `work_summary` — what you actually changed
- `learnings` — gotchas, surprises, insights

In practice only **`learnings`** is load-bearing across tickets — it's the field that feeds `search_learnings` and shapes future agent behaviour. `testing_evidence` and `work_summary` are useful in-context audit trail for the ticket they belong to, but they don't earn their keep against future search.

The cost of requiring all three:

- Models pad `testing_evidence` and `work_summary` to hit the ≥10 char gate when the change is trivial ("ran existing tests, all passed" / "one-line typo fix"). The padded prose dilutes the embedding signal across the completion vector.
- The two fields often paraphrase each other — what was tested and what was changed converge on small tickets.
- Total payload size grows, making the MCP envelope more fragile (related to but not the cause of [[claude-code-complete-ticket-envelope-bug]]).

## Proposal

Reshape `complete_ticket` so:

- `learnings` stays required (≥10 chars). This is the field that earns its place across tickets.
- `testing_evidence` and `work_summary` become **optional**. Provide them when there's substantive content; omit when there isn't.
- Drop the ≥10 char minimum on the two non-learnings fields. If supplied, accept any non-empty string. (Or: relax to ≥1 char; the gate exists to prevent "x" entries, but with optional we can drop it entirely.)
- Tighten the tool description to make the field-priority explicit: "`learnings` is what future agents search — write that for them. The other two are audit-trail and may be omitted on small/obvious work."

## Out of scope

- Combining the three fields into one. Keeping them separate preserves search precision (learnings stays its own embedded vector) and keeps the completion.md structure stable.
- Allowing learnings to be empty. The whole feedback loop falls over if learnings can be skipped.
- Multi-call completion. Adds round-trips and partial-state failure modes for no real win; would not work around the MCP transport bug.

## Touchpoints

- `internal/mcptools/tools.go` — `complete_ticket` tool registration around the existing `mcp.WithString("testing_evidence", mcp.Required(), ...)` calls. Drop `mcp.Required()` from the two; update descriptions.
- `internal/mcptools/tools.go` — `handleCompleteTicket`. Switch `requireStringArgs` to only require `ticket_id` + `learnings`; pull the two others optimistically.
- `internal/svc/service.go` (or wherever `CompleteTicket` lives) — accept empty `testing_evidence` / `work_summary`; keep ≥10 char gate on `learnings`.
- `internal/store/` ticket write — write empty optional fields as empty / omitted in `completion.md` rather than literally storing "".
- `internal/mcptools/complete_ticket_richfield_test.go` and the broader complete_ticket test suite — add cases for (a) only learnings supplied, (b) all three supplied (existing happy path), (c) empty learnings rejected with a clear message.
- `SPEC.md` — update the completion-fields description and the "completion is structured and sacred" line in CLAUDE.md if it claims all three are required.
- Server `instructions` text returned by `Initialize` (the long blob in `cmd/.../serve.go` or wherever it's defined) — update the "When done, call `complete_ticket` with substantive `testing_evidence`, `work_summary`, and `learnings`…" reflex.

## Acceptance

- Calling `complete_ticket` with `ticket_id` + `learnings` (≥10 chars) succeeds and the resulting ticket has empty/null `testing_evidence` and `work_summary`.
- Calling with all three still succeeds and round-trips full content (existing TestCompleteTicket_LongRichFields_MapEnvelope test stays green).
- Calling with empty/missing `learnings` is rejected with a clear, accurate error naming `learnings` (not the misleading multi-field message).
- `get_ticket` on a learnings-only completion renders cleanly in the web UI (no broken "Testing evidence:" header with empty body).
- Tool description and `Initialize` instructions read consistently: learnings required, others optional with the why.

## Background

Surfaced in conversation 2026-05-30 while debugging the `complete_ticket` failure pattern. Direct curl tests proved the rich-payload error is a client-side MCP envelope bug, not a server validator bug. Separately, the schema's three-required-fields shape was identified as over-asking — most tickets don't have meaningfully separable content across the three. This ticket addresses the schema; the envelope bug is upstream in Claude Code.
