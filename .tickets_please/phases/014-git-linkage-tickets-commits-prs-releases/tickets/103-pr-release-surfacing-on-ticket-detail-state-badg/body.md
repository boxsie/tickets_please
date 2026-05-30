Surface PR state (open/merged/closed/draft) and release pills on every ticket that's been shipped.

## Acceptance

- "PRs" sub-section in the Commits card (or its own card): each linked PR shown with title, number, state-badge (open/draft/merged/closed/review_required), author, last-updated. Links to PR on GitHub.
- PR linkage detection: (a) PR body contains `Resolves tickets-please/NNN` / `Fixes tickets-please/NNN` / `Closes tickets-please/NNN` (configurable list), OR (b) PR's source branch matches `ticket/NNN-*` per [[branch-indexer]].
- "Shipped in" pills: render in the ticket-detail metadata block (next to status badge) listing each release the ticket's commits appear in. Pill links to the release page on GitHub + to the in-app `/releases/{tag}?slug={project}` view.
- Release detection: walk tags / `gh release list`; for each release, walk commits between this tag and the previous tag; collect all ticket refs in that range; reverse-map to tickets.
- New page `/p/{slug}/releases` listing all releases, each showing "what shipped" (tickets, grouped by phase).
- Tests cover: PR detection from both signals; release reverse-mapping; empty-state.

## Hints

- For "what shipped" grouping, fetch ticket-by-id in batches for the union of refs across the range.
- Limit displayed PRs to most-recent N; "show all" expander reveals the rest.
