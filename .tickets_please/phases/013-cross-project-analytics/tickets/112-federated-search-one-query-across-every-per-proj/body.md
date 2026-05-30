A global "where in any of my projects has this come up?" search. Each per-project vec adapter answers independently; results merged + ranked across projects with project-context annotated on each hit.

## Acceptance

- New route `GET /search?q=…&kind=…` (global, no `/p/{slug}/` prefix). Sidebar entry.
- Fan-out: for each project the user has membership to, call the existing per-project `search_tickets` / `search_learnings` / `search_comments` (top-K per project, default K=10).
- Merge hits client-side (in svc): every hit annotated with `project_slug` + `project_name`. Re-rank by raw cosine score (no cross-project normalisation v1 — embedders may differ; document the caveat). Show top 50 merged.
- Kind tabs preserved (tickets/learnings/comments).
- Each hit visually shows its source project as a small pill before the title.
- Tests cover: fan-out across projects, user-membership filter, kind switching, empty-query state.

## Hints

- Use `errgroup` to parallelise per-project searches; on any project's error, log + continue (don't fail the whole search).
- Per-project results are cached for ~10s to avoid hammering embedders if the user retypes.
