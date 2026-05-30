## Phase: Git linkage

Today, `tickets_please` commits its own mutations to git (you can see `[tickets_please] complete ticket tickets-please/063 [Claude Code]` in the log). But feature-implementation commits aren't linked back to the ticket they implemented. There's no "ticket #63 was built in these 5 commits across these 3 days" view, no PR linkage, no release linkage, and analytics can't know how much code a ticket actually shipped.

This phase adds automatic two-way linkage: ticket → commits/branches/PRs/releases that implemented it, and commit message → ticket.

## Two deployment modes, one design

The MCP server runs in two shapes and the design has to work for both:

**Local (stdio):** server is in the working tree; it can shell out to `git` directly. Walks the local log, installs hooks, no extra config.

**Remote (HTTP, e.g. tickets_please on a remote host):** server only has the `.tickets_please/` mount — not the working tree. So it can't shell out to `git` against the user's code. Instead:
- Each project declares its git remote in `project.yaml` (`git.remote: github.com/...`).
- Server uses the user's GitHub OAuth token (obtained during the Phase 1 W2 OAuth flow) to call the GitHub API for commits, PRs, branches, releases.
- Realtime PR/release events arrive via a signed GitHub webhook at `/webhooks/github`.

Non-GitHub git providers (gitea, plain bare repo, GitLab) are out of scope for this phase. If we need them later we'll add provider implementations behind the `git.Provider` interface or revisit the sidecar approach — but for now, "remote tickets_please requires GitHub-hosted code" is an accepted constraint.

## Auto-attribution

Two ways the system learns which commits belong to which ticket:

1. **Commit message footer convention.** Agents include a `tickets-please/NNN` footer (the slug+number) in feature commits. Parser scans every commit's message for this token. The `prepare-commit-msg` hook installed by the new `tickets_please commit-hook install` subcommand auto-injects this footer based on the agent's currently in-progress ticket — so attribution is automatic and the LLM doesn't have to remember.
2. **Branch naming convention.** Branches named `ticket/NNN-*` or `tickets-please/NNN-*` are auto-associated with their ticket; the per-ticket panel shows them as "active branches".

PR linkage piggybacks on both: detect "Resolves tickets-please/NNN" in PR body, or fall back to branch-name match.

## Release linkage

Walk `git tag` (local mode) or `gh release list` (remote mode). For each release, walk its commit range, look up which tickets those commits referenced. Per release: show "what shipped" as a ticket-grouped changelog. Per ticket: show "Shipped in v1.4.0" pill.

## Hard rules

- Commit→ticket index is server-side, lazily built on first access, refreshed on push events.
- Index is rebuildable from scratch (commit messages + tags are the source of truth; the index is a cache).
- Webhook signature verified via GitHub's HMAC-SHA256; unsigned requests rejected.
- Hook installer is opt-in; it adds to (never replaces) any existing `prepare-commit-msg` hook.
- Tickets don't gain editable git fields — links are derived, not authored.

## Waves

```
Wave 1 — Provider abstraction + indexers (server-side)
Wave 2 — Surface git on tickets (UI)
Wave 3 — Auto-attribution + webhook + realtime
```
