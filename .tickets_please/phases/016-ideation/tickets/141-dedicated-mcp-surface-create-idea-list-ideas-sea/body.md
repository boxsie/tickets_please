Give ideas their own front door — the "its own place to live" requirement. These are thin wrappers over the existing ticket store with `kind=idea` baked in; no new storage.

## Changes (`internal/mcptools/tools.go`)
- **`create_idea`** — lightweight: `title` required, `body` optional, `project_id_or_slug` optional (session default). No `depends_on`/`wave`/phase nagging — a spitball should be one call. Internally `CreateTicket` with `Kind = KindIdea` (and `Column = todo`).
- **`list_ideas`** — `list_tickets` pinned to ideas only (forces the kind filter to ideas, ignores `include_ideas`). Optional `project_id_or_slug`, pagination like `list_tickets`.
- **`search_ideas`** — `search_tickets` pinned to ideas only.
- Update the **three-place lockstep tool count** + canonical-list test (learning #a28797b8) for the three new tools (plus `promote_idea` from the sibling ticket — coordinate the count).

## Notes
- Keep these as handlers that build the shared `domain.*Input` with kind pinned, rather than duplicating service logic — one source of truth in `svc`.
- Tool descriptions should teach the model the concept: ideas are spitballs, hidden from normal work, promote with `promote_idea` when they mature.
- `complete_ticket`/`move_ticket` deliberately have NO idea-pinned variants — the only forward path for an idea is `promote_idea`.

## Acceptance
- `create_idea` makes a hidden idea (absent from default `list_tickets`).
- `list_ideas` / `search_ideas` return only ideas.
- Tool-count test passes.
- End-to-end via local MCP (use a throwaway): create_idea → list_ideas shows it → list_tickets hides it → promote_idea → list_tickets shows it, list_ideas no longer does.
