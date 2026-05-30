## Phase: Search feedback & archival

Semantic search across tickets, learnings, and comments is the system's load-bearing memory loop. As any project accumulates work, that loop has two failure modes that aren't addressed today:

1. **No quality signal.** Every result is ranked purely by cosine similarity to the query embedding. A learning that was wrong, or a ticket whose context has rotted, ranks the same as something genuinely useful — the consumer LLM has no way to teach the index "this was the right one" or "this misled me," and the index has no way to learn.
2. **Stale ≠ low-quality but both pollute.** Old work that's been superseded keeps appearing in top-k. There's no archival path, so the noise floor rises monotonically.

This phase introduces a small feedback loop (likes/dislikes from consumers, weighted into ranking) and an archival policy that uses both age and feedback signal to retire results from the default search surface (still retrievable via an explicit `include_archived` flag).

## Decided design

### Feedback model

Per-result aggregate counters live in a per-project YAML store (`.tickets_please/feedback.yaml`), keyed by the vec-index entry key (`<kind>:<id>` — e.g. `learning:51153b49-...`, `ticket:abc...`, `comment:xyz...`). Each record holds `{likes, dislikes, last_feedback_at, last_used_at}`. YAML for grep-ability and git-tracking, matching the rest of the on-disk store. Per-result aggregate (not per-(result, query)) because queries are free-text and rarely exact-match; query-aware weighting is a future enhancement we'll consider once we have feedback data to look at.

### Weighting

Bayesian-smoothed prior: `quality = (likes + α) / (likes + dislikes + α + β)` with `α=β=2` (mild "innocent until rated" prior — one downvote can't nuke a result). Final score = `cosine_similarity × (0.5 + 0.5 × quality)` — quality acts as a 0.5×–1.0× multiplier, so it tilts ranking without overwhelming the embedding signal. Constants live in project config so they can be tuned without code changes.

### Feedback capture

New MCP tool `rate_search_result` taking `{entry_keys: ["learning:abc...", ...], rating: "like" | "dislike", reason?: string}`. Reason is optional free text, stored alongside the counters for human review (not currently fed back into ranking). Search response payloads grow a small `feedback_hint` block listing the entry keys and a one-line "rate these with rate_search_result" nudge — the goal is to make feedback the natural next step, not a discoverable hidden feature.

### Archival

Per-project archive policy in `project.yaml`:
```yaml
archive:
  enabled: false  # opt-in per project
  min_age_days: 180
  min_retrievals: 3       # must have appeared in N searches before age applies
  dislike_ratio: 0.5      # dislikes / (likes + dislikes) over threshold → archive early
  early_archive_age_days: 30  # but not earlier than this
```

A new `archived` boolean on tickets (and on completion learnings indirectly via their parent ticket). Archived items are **excluded by default** from all three searches; a new `include_archived: bool` parameter on `search_tickets` / `search_learnings` / `search_comments` brings them back when explicitly requested. Vec-index entries stay in place — exclusion is a post-filter, so unarchive is free.

The sweep is manual + automatic: a new `apply_archive_policy` MCP tool returns a dry-run report by default and applies on `commit=true`; an `archive.auto_sweep_on_mount` config knob (default false) runs the policy on each mount hydrate.

## Hard rules

- Archive is reversible (a new `unarchive_ticket` tool, audited like any column move with a required comment). Done tickets stay frozen — archive flips a separate `archived` flag, not the column.
- Feedback writes are append-only counter updates; no "edit a rating" path. To revise, send the opposite rating — net effect on the prior is what counts.
- `feedback.yaml` is git-tracked (same as `project.yaml`, `summary.md`); the embedding sidecars stay gitignored.

## Tickets in this phase

```
Wave 1 — Feedback capture
  T1 Per-project feedback store + entry-key scheme
  T2 rate_search_result MCP tool
  T3 Surface entry_keys + feedback_hint in search responses

Wave 2 — Weighted ranking
  T4 Apply Bayesian-smoothed quality multiplier to search scoring

Wave 3 — Archival
  T5 Archive policy schema + archived flag + include_archived on searches
  T6 apply_archive_policy tool + optional auto-sweep on mount
```

Waves are sequential — W2 needs the W1 store to read from; W3's signal-based early-archive needs W1's counters and W2's scoring concepts.

## Out of scope

- Per-(result, query) feedback (revisit if aggregate signal turns out to be too coarse).
- Cross-project feedback aggregation.
- LLM-assisted "supersedes" relationships (the `reason` field captures intent in free text for now; structured supersede edges can come later).
- Decay (a year-old like worth less than a recent one) — keep the formula boring until we have data.

## Critical files (anticipated)

- `internal/svc/search.go` — `SearchTickets` / `SearchLearnings` / `SearchComments` scoring path
- `internal/svc/mounts.go` / `internal/cache/projectcache.go` — load `feedback.yaml` per mount
- `internal/store/feedback.go` (new) — the per-project feedback store
- `internal/domain/feedback.go` (new) — the record type + entry-key helpers
- `internal/mcptools/tools.go` — three new tools (`rate_search_result`, `apply_archive_policy`, `unarchive_ticket`); `totalTools` in `cmd/tickets_please/main.go` + `expectedTools` in `mcptools/tools_test.go` move in lockstep
- `internal/domain/project.go` — `ArchiveConfig` on project.yaml
- `SPEC.md` — document feedback + archive in the MCP tool tables and the on-disk store section
