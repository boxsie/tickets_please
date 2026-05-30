## Goal

Make `rate_search_result` discoverable and the natural next step after a search. If the response doesn't tell the LLM the entry keys (and that rating them is a thing it can do), nobody will use the feature and we won't get the signal that W2/W3 need.

## Scope

### Add `entry_key` to every search hit

`search_tickets`, `search_learnings`, `search_comments` already return hits with `id`, `score`, `title`, etc. Add an `entry_key` field to each hit, computed via the T1 helpers (`domain.TicketEntryKey` etc.). The entry key is what `rate_search_result` accepts, so the consumer doesn't have to construct it themselves.

### Add `feedback_hint` to the top-level search response

```json
{
  "hits": [...],
  "feedback_hint": {
    "tool": "rate_search_result",
    "entry_keys": ["learning:abc...", "ticket:def...", ...],
    "note": "If any of these were useful or misleading, rate them with rate_search_result so future searches improve."
  }
}
```

`entry_keys` is the flat list of all hit entry keys, in result order. The note is fixed text — short, action-oriented, mentions the tool by name.

### Record retrievals (feeds W3)

When a search returns N hits, call `feedback.RecordRetrieval(entryKeys)` on the project's feedback store before returning. This bumps `retrievals` and `last_used_at` for each entry. T1 provides the API; this ticket wires the call.

Performance: single store write per search call (batched), not one per hit. The store lock is per-project, so concurrent searches on different projects don't contend.

### Don't surface feedback counts in hits

Tempting to show `{likes: 3, dislikes: 0}` per hit but skip for now — it would encourage gaming and bloats the response. Counts are observable by reading `feedback.yaml` directly when debugging.

### Tests

- A `search_learnings` call returns hits each with a parseable `entry_key`.
- The top-level `feedback_hint.entry_keys` matches the order of `hits`.
- Calling `search_*` increments retrievals for every returned entry (assert via direct store read).
- An empty-result search returns no `feedback_hint` (don't nag when there's nothing to rate).

## Out of scope

- A separate `get_feedback` tool to inspect counts — `feedback.yaml` is grep-able, no tool needed yet.
- Per-hit `quality` field (computed in W2 from the same store; visible via score adjustment).

## Critical files

- `internal/svc/search.go` — `SearchTickets` / `SearchLearnings` / `SearchComments` return paths
- `internal/mcptools/tools.go` — the three search-tool handlers' response formatters
- `internal/mcptools/format.go` — if hit serialisation lives here, the `entry_key` add lands here
- Whatever response struct is shared (`SearchHit` in svc and the MCP-side mirror)

Depends on T1 (entry-key helpers + store) and T2 (tool name to point the hint at).
