## Goal

Expose a single MCP tool that lets the consuming LLM rate one or more search results, hitting the store from T1.

## Scope

### Tool signature

```
rate_search_result({
  entry_keys: ["learning:51153b49-...", "ticket:abc...", ...],  // required, 1..50
  rating: "like" | "dislike",                                    // required
  reason?: string                                                // optional, max 500 chars
})
```

Returns `{updated: [{entry_key, likes, dislikes}], rejected: [{entry_key, error}]}`. Per-key partial success — a malformed key fails just that key, not the whole call.

### Semantics

- One `rate_search_result` call applies the same rating to every key passed. Mixed ratings → multiple calls.
- Unknown entry keys (no such ticket/learning/comment in this project) → rejected with `"unknown entry"`.
- Rating any one key bumps its counter once per call. Repeated calls = repeated bumps (no idempotency key; the model is "every call is a fresh thumbs-up").
- `reason` is appended to the entry's `Reasons` slice (capped at last 10).
- Updates `last_feedback_at = now`.

### Validation

- `entry_keys`: non-empty, ≤50.
- `rating` ∈ {`like`, `dislike`} (exact match).
- `reason`: optional, ≤500 chars (truncate with a warning rather than reject — the goal is to make feedback friction-free).
- Entry keys must parse via `domain.ParseEntryKey` and resolve to a real object in the bound project.

### Wiring

- New svc method `(*Service).RateSearchResult(ctx, projectIDOrSlug, RateInput) (RateOutput, error)`. Uses session-bound project by default per `callWithRetry` pattern.
- New handler in `internal/mcptools/tools.go` registered alongside the other tools.
- Bump `cmd/tickets_please/main.go:totalTools` and `internal/mcptools/tools_test.go:expectedTools` (32 tools now).
- README + SPEC.md `## MCP server` table get a row for the new tool.

### Audit trail

Don't add a per-rating comment to the ticket (would be noisy at scale). Instead, feedback lives in `feedback.yaml` and shows up in git history when the file is committed. Document this in SPEC's audit-trail section.

### Tests

- Happy path: like + dislike, counters increment, response shape correct.
- Partial-success: 3 valid keys + 1 unknown → updated has 3, rejected has 1.
- Reason capping: 11th reason pushes the oldest out.
- Rate-limit / abuse: not in scope — local trusted use only.
- Concurrent rates on the same key from two sessions (use `errgroup`): both increments land, no lost updates (relies on T1's file lock).

## Out of scope

- Surfacing the tool to the LLM via search response hints — T3.
- Weighting the score — W2.
- A `rate_search_result_bulk` variant — already handles bulk via the `entry_keys` array.

## Critical files

- `internal/svc/search.go` or new `internal/svc/feedback.go` — `RateSearchResult` method
- `internal/mcptools/tools.go` — tool registration + handler
- `internal/mcptools/tools_test.go` — `expectedTools` bump
- `cmd/tickets_please/main.go` — `totalTools` bump
- `SPEC.md` + `README.md` — tool table row
