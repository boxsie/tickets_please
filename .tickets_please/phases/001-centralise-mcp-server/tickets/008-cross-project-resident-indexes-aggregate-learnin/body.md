## Goal

The resident `learnings_index` and `summaries_index` (currently global across all projects under one data dir) become global across all *mounted projects* in the centralised server. They aggregate as projects mount; entries drop as projects unmount.

## Why

The "search across all your work" superpower is a tickets_please core feature. With multi-root storage (one project per repo), the resident index has to span repos to keep that property.

## Scope

- `internal/svc/service.go`:
  - `learningsIndex` and `summariesIndex` are still resident, but now keyed by `(slug, ticketID)` rather than just `ticketID`. Project provenance is part of every result.
  - On `RegisterProjectMount(slug, repoPath)`: load that project's learning embeddings + summary embeddings from `<repoPath>/.tickets_please/`, add to index.
  - On project unmount/eviction: drop that project's contributions from the index.
  - Locking: writes to the resident index are sync.Mutex-guarded (already are in current code, just making sure it survives the refactor).
- `internal/svc/search.go`:
  - `SearchLearnings(query, projectFilter?)` returns hits across all mounted projects when filter is empty; restricts to one when given.
  - `SearchProjects(query)` ranks across all mounted projects.
  - Result schema includes the `project_slug` so the LLM can navigate to the right project.

## Index hydration

- On mount, scan `<repoPath>/.tickets_please/{tickets,phases/*/tickets}/<ticket>/completion.embedding.json` for learnings, `<repoPath>/.tickets_please/{summary,phases/*/summary}.embedding.json` for summaries.
- If embeddings are missing (e.g. fresh repo, or migrated from old layout), enqueue them on the existing embed worker.

## Verification

- Mount two projects with completion learnings. `search_learnings` (no project filter) returns hits from both, ranked by similarity.
- Same with `search_projects`.
- Unmount one project. Re-run search — only the remaining project's hits appear.
- Re-mount it. Hits return.
- Race-detector run with concurrent mount + search.

## Notes

- The dimension-mismatch concern from the OpenAI/Ollama swap (per current SPEC) still applies — switching providers requires deleting all `*.embedding.json` files. No new logic; just confirm the centralised path doesn't break the existing safeguard.
- This ticket is the most likely place for performance issues if many projects mount. Spot-check memory + index build time with ~50 mounted projects of ~100 tickets each. If it's a problem, defer the lazy-load split to a follow-up ticket.
