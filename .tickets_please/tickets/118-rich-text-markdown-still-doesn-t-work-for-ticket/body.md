## Symptom (reported by user)

> "They still moan about rich text not working for ticket text."

Agents report that rich / multi-paragraph markdown in **ticket body** text (and plausibly comments / update bodies) fails or gets mangled when set via the MCP tools — `create_ticket` / `update_ticket` (and possibly `add_comment`, `move_ticket` comments).

## Strong suspect: the same root cause as the complete_ticket bug

Ticket `5688ace3` (complete_ticket rejects long/rich fields with misleading "required argument not found") root-caused this to the **mcp-go `RequireString` contract**: it reads args only via `GetArguments()` → `Params.Arguments.(map[string]any)`, returning nil for any other envelope shape (e.g. `json.RawMessage`). When the client delivers a long/rich payload that arrives un-pre-decoded, every field reads as "not found" even though it's present. The fix was `requireStringArgs` (tries the map fast-path, falls back to `BindArguments`).

That ticket's learnings explicitly flagged: **"The same fragility affects EVERY handler still using `RequireString` (move_ticket, add_comment, register_agent, search_*, etc.). Only complete_ticket was in scope here; if the misleading-error class resurfaces on another tool, port it to `requireStringArgs`."**

This is that resurfacing. So:

## Work

1. Audit every MCP handler that still calls `req.RequireString` for a free-text field — especially `create_ticket` (title, **body**), `update_ticket` (body), `add_comment` (body), `move_ticket` (comment). Grep `internal/mcptools/` for `RequireString`.
2. Port them to the envelope-robust `requireStringArgs` helper (already in `internal/mcptools/tools.go`).
3. Add regression tests mirroring `complete_ticket_richfield_test.go`: a `RawMessageEnvelope` case carrying >1KB markdown (fenced code blocks, nested lists, **bold**, headings, unicode/emoji) through `create_ticket` / `update_ticket`, asserting the body round-trips intact.
4. Confirm the goldmark render pipeline on the web side handles the body fine (it already does for existing tickets — the failure is the arg-decode boundary, not rendering).

## Open question

Confirm whether the complaint is specifically the **set** path (MCP envelope, above) or the **render** path (web display). The envelope path is the high-confidence bet given the prior root-cause; verify by reproducing a `create_ticket` with a rich body and seeing whether it errors with the misleading "required argument not found".

## Acceptance

- `create_ticket` / `update_ticket` accept long, rich, multi-paragraph markdown bodies (code fences, nested lists, bold, unicode) without error and round-trip them intact.
- Regression test covering the raw-envelope rich-body case.
- Any remaining `RequireString` free-text handlers ported to `requireStringArgs` (or explicitly justified as safe).
