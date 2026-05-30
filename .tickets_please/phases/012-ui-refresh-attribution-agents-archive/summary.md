## Phase: UI refresh — attribution, agents, archive

The domain already stores who created/completed every ticket and when, and the search-feedback-archival phase added archived flags + per-result feedback. None of this is surfaced in the UI. This phase fixes that and adds the long-missing agents page. Built on the new templ + Tailwind + Datastar + SSE bones from the frontend-modernisation-foundation phase — so live attribution, live agent activity, etc. fall out for free.

## Goals

- Author + relative time visible on every ticket reference, with hover-absolute. Same on phases, comments are already there.
- Ticket-detail metadata block: created_by/at, completed_by/at, updated_at, archived state, dependencies, parallelizable_with, entry key.
- Global `/agents` page listing every registered agent with their work — tickets created, tickets completed, comments authored, sorted by recency.
- Surface the search-feedback-archival features that are currently MCP-only: archived badge + un/archive actions, include_archived toggle on search and lists, 👍/👎 buttons on search hits, archive-policy form + dry-run.
- Kill the Trello board page (useless at scale per user); phases→waves is the spine. Deep-link to waves.

## Out of scope

- Cross-project analytics — that's its own phase next.
- Realtime push for archive flips, ratings, etc. (foundation only delivers it for column moves/comments/agent activity; specific live patches for the new surfaces here are a follow-up if anything feels static).

## Waves

```
Wave 1 — Spine (kill board, deep-link waves, project overview tweak)
Wave 2 — Attribution everywhere (ticket metadata block, author/time on all references, shared reltime helper)
Wave 3 — Agents page (ListAgents svc, /agents index, /agents/{id} activity)
Wave 4 — Surface backend (archive UI, include_archived toggle, search ratings, archive-policy settings)
```
