---
id: T11
title: Search methods (semantic)
status: DONE
owner: subagent-T11
depends_on: [T10]
parallelizable_with: []
wave: 7
files:
  - internal/svc/search.go
estimate: small
stretch: false
---

# T11 — SearchService

## Scope

Implement the four semantic search RPCs over the in-memory `vecindex`. Each handler embeds the query and calls `Index.Search`, then hydrates results from the store.

**In:** `internal/svc/search.go` with all four search methods on `Service`.

**Out:** No re-ranking, no hybrid lexical+vector, no query rewriting. Brute-force cosine top-k is plenty.

## Files

- `internal/svc/search.go`

## Details

### Handlers

Each RPC follows the same shape:

1. Validate `query` non-empty after trim. Cap `limit` at 50; default 10.
2. `vec, err := server.Embed.Embed(ctx, req.Query)`.
3. Call the right `Index.Search(...)` with the right `kind` and `ownerFilter`.
4. Hydrate hits from the store (fetch the source row, build the domain struct).

### Per-RPC specifics

**`SearchProjects(query, limit)`**
- Use the resident `projects_summary_index`.
- Filter: `kind = ProjectSummary`. No owner filter.
- Hydrate: load each project's `project.yaml` + `summary.md`, build `Project` message.
- Return `repeated ProjectHit { Project project; float score }`.

**`SearchTickets(query, project_id_or_slug?, columns?, limit)`**
- If `project_id_or_slug` is set, ensure the project is loaded (lazy auto-load via T04's project cache), then search the per-project index.
- If unset, walk loaded projects and merge results, OR (simpler) require it for v1 — if v1 path: return `InvalidArgument` when `project_id_or_slug` is empty.
- Apply optional `columns` filter post-hoc by checking each hit's `Ticket.column`.
- Return `repeated TicketHit { Ticket ticket; float score }`.

**`SearchComments(query, project_id_or_slug?, ticket_id?, limit)`**
- Same auto-load + per-project index pattern as SearchTickets.
- If `ticket_id` is set, post-filter hits to comments owned by that ticket.
- Return `repeated CommentHit { Comment comment; float score; string ticket_title }`.

**`SearchLearnings(query, project_id_or_slug?, limit)`**
- Use the resident `learnings_index`.
- If `project_id_or_slug` is set, post-filter hits where the ticket's project matches. (`Entry.Owner` carries the slug.)
- Return `repeated LearningHit { ticket_id; project_id; title; learnings; score; completed_at }`.

### Score interpretation

`vecindex.Search` returns scores in `[0, 1]` (cosine, assuming normalized vectors). Pass them straight through to the result `Score` field. No transformation.

### Empty-state behavior

If the relevant index has zero entries, return an empty list — **not** an error. "No results" is a valid answer.

## Acceptance criteria

- [ ] Seed: create 3 projects with distinctive summaries; `SearchProjects("query that paraphrases project A's summary")` returns project A as the top hit with score > 0.5.
- [ ] Complete a ticket with distinctive learnings. `SearchLearnings("paraphrase of those learnings")` returns it as top hit.
- [ ] `SearchTickets(query=…, project_id_or_slug=foo)` returns only tickets in project foo.
- [ ] `SearchComments(query=…, ticket_id=Y)` filters to comments on ticket Y.
- [ ] Empty query → `InvalidArgument`.
- [ ] Search before any embeddings exist returns an empty list (not an error).
- [ ] `SearchTickets` without `project_id_or_slug` on v1 returns `InvalidArgument` with a message recommending the project filter.

## Notes

See **Vector search index** and **Service API > Search** in [`../SPEC.md`](../SPEC.md). All search reads go through the in-memory `vecindex`; no scanning sidecar files at request time. T04's project cache transparently handles auto-loading when a search references an unloaded project.
