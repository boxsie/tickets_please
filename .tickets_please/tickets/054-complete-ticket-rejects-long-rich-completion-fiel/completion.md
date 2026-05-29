## Testing evidence
Added `internal/mcptools/complete_ticket_richfield_test.go` with three cases, all green (`go test ./internal/mcptools/ -run TestCompleteTicket` → ok):

1. **MapEnvelope** — completes a ticket whose three fields each carry a >1KB multi-paragraph markdown payload (fenced ```go code block, nested bullet lists, **bold**, `### headings`, unicode/emoji 🎟️/你好). Asserts column=="done" and that the code fence + emoji survive the storage round-trip.
2. **RawMessageEnvelope** — drives `handleCompleteTicket` with `Params.Arguments` set to a `json.RawMessage` (not a pre-decoded map). This is the exact envelope shape that made `GetArguments()` return nil and `RequireString` mis-report present fields as "not found". Now passes via the BindArguments fallback.
3. **MissingFieldAccurateError** — omits work_summary+learnings, asserts the error names BOTH missing fields rather than the misleading single "work_summary not found".

Full package `go test ./internal/mcptools/` → ok (no regressions). `go build ./...` clean. Also dogfood-verified: this very completion call carries long, code-fenced, bulleted markdown through the local stdio MCP and succeeded.

## Work summary
`internal/mcptools/tools.go`:
- Added `requireStringArgs(req, keys...)` — an encoding-robust required-string extractor. It first tries `req.GetArguments()` (the map fast path); if that is nil (Arguments arrived as a non-map envelope such as `json.RawMessage`), it falls back to `req.BindArguments(&map[string]any{})`, which marshals whatever Arguments holds back to JSON and re-decodes it. It collects ALL missing fields and reports them together ("required argument(s) not found: a, b"), and distinguishes wrong-type ("argument %q must be a string, got %T"). No length/format cap anywhere.
- Refactored `handleCompleteTicket` to decode ticket_id/testing_evidence/work_summary/learnings through `requireStringArgs` instead of four separate `req.RequireString` calls.

`internal/mcptools/complete_ticket_richfield_test.go`: new regression test (3 cases described in testing evidence) plus a `richCompletionField` fixture and a `seedTicketForCompletion` helper.

## Learnings
Root cause was NOT a length/format cap and NOT in this repo's validation (svc validation only enforces a ≥10 char MINIMUM, no maximum). It is the mcp-go (v0.50.0) `CallToolRequest.RequireString` contract: it reads args solely through `GetArguments()`, which does `Params.Arguments.(map[string]any)` and returns nil for ANY other shape. When a transport/client delivers arguments as `json.RawMessage` (or otherwise un-pre-decoded), `GetArguments()` is nil and `RequireString` returns the literal "required argument %q not found" for every field — present or not. So the misleading message is a shape mismatch, not a missing field.

Key gotchas for future-you:
- `RequireString` says "not found" for ABSENT keys and "is not a string" for present-but-wrong-type. If you ever see "not found" for a field you KNOW you sent, suspect the Arguments envelope shape, not your payload.
- `req.BindArguments(&target)` is the robust decode: it has a `json.RawMessage` fast-path and otherwise `json.Marshal`s Arguments then unmarshals — so it survives both map and raw envelopes. Prefer it (or the new `requireStringArgs` helper) over `RequireString` for any handler that must be envelope-agnostic.
- The same fragility affects EVERY handler still using `RequireString` (move_ticket, add_comment, register_agent, search_*, etc.). Only complete_ticket was in scope here; if the misleading-error class resurfaces on another tool, port it to `requireStringArgs` — it's generic over keys.
- Normal stdio/HTTP JSON-RPC in mcp-go decodes Arguments to map[string]any (no custom UnmarshalJSON on CallToolParams), so the map path is the common case and existing tests that set `Params.Arguments = map[string]any{...}` never exercised the raw path. The new RawMessage test closes that gap.
