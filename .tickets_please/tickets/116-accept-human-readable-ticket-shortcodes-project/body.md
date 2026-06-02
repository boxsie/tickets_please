## Symptom (reported by user)

> "Agents always try to access tickets via the shortcode and then realise they can't."

Every ticket-targeting MCP tool (`get_ticket`, `move_ticket`, `complete_ticket`, `add_comment`, `update_ticket`, `assign_ticket_to_phase`, `delete_ticket`, `archive_ticket`, …) takes a `ticket_id` that must be the **UUID**. But agents see and reason about tickets by their **shortcode** — `tickets-please/076` — because that's the human-readable handle that shows up everywhere: the global ticket number, commit messages (`[tickets_please] complete ticket tickets-please/076`), and how humans refer to tickets in conversation. So the first instinct is to call `get_ticket(ticket_id="tickets-please/076")`, it fails, and the agent has to go list/search to recover the UUID. Pure friction, every time.

## Proposed fix

Make `ticket_id` accept **either** form:

- a bare UUID (today's behaviour), or
- a shortcode `"<project-slug>/<number>"` (e.g. `tickets-please/076`), or possibly even a bare number when a project is already bound to the session via `register_agent`.

Resolve the shortcode to a UUID at the MCP-handler boundary (or in svc) before the existing logic runs, so every tool benefits from one resolver rather than N call-site changes.

## Notes / hints

- Ticket numbers are **global, max-wins** (see CLAUDE.md), and the on-disk layout already carries the number — there's a number→ticket mapping available without a full walk if we want it fast, but an O(tickets) lookup is fine at this scale to start.
- The session is usually bound to a project slug already (`register_agent`), so `"076"` alone could be unambiguous in the common case; `"<slug>/<num>"` removes ambiguity for cross-project/unbound sessions.
- Mirror the envelope-robust arg handling already in place — see `requireStringArgs` (added in the complete_ticket envelope fix) so the resolver doesn't reintroduce the `RequireString` fragility.
- Error message when resolution fails should be actionable: "no ticket `tickets-please/999` in project `tickets-please`" rather than a generic not-found.

## Acceptance

- `get_ticket(ticket_id="tickets-please/076")` returns the same ticket as passing its UUID.
- Same for the other ticket-targeting tools (at minimum the read + move/comment/complete path).
- A bare UUID still works unchanged.
- Bad shortcode → clear, specific error naming the slug + number.
- Tool descriptions updated to say `ticket_id` accepts a UUID **or** a `project-slug/number` shortcode, so agents are told up front.
