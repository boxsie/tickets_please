## Problem

`list_comments` only takes `ticket_id` (one ticket at a time). To find user-authored comments across an entire project — e.g. while sweeping operator feedback on a phase of newly-filed tickets — there's no direct way. Today the workaround is:

1. Call `search_comments` with a vague query like "feedback note concern".
2. Get a 100 KB+ JSON dump dominated by system-generated move/completion comments authored by Claude Code itself.
3. Filter in a sub-process by `author.name != "Claude Code"` and `body NOT LIKE 'Ticket moved%' AND NOT LIKE '✅ Ticket completed%'`.
4. Repeat with different keywords because semantic search doesn't have a "list all" mode.

This pattern just played out during the serves security audit follow-up — 35 tickets had been filed minutes earlier; the operator added 6 comments via the web UI; finding them required two `search_comments` calls + a Python filter script. A direct `list_comments` that accepts a `project_id_or_slug` (and/or `phase_id_or_slug`) would have been one call.

## Proposed tool: `list_comments_scoped`

Parameters (all optional except one of project/phase/ticket scope MUST be set):

| param | type | purpose |
|---|---|---|
| `project_id_or_slug` | string | scope to a project (session default if unbound) |
| `phase_id_or_slug` | string | scope to a phase (within project) |
| `ticket_id` | string | scope to a single ticket (redundant with existing `list_comments` but accepted for orthogonality) |
| `author_id` | string | filter by exact author UUID |
| `author_name` | string | filter by exact author display name (e.g. `"Web UI"`, `"Claude Code"`) |
| `exclude_author_id` | string | inverse of `author_id` — "everything NOT mine" |
| `exclude_system` | bool (default `true`) | drop auto-generated comments: `Ticket moved to …`, `✅ Ticket completed`, `kind: system` |
| `kind` | string | filter by comment kind (`user`, `system`, `move`, `completion`) if those are distinct |
| `since` | RFC3339 timestamp | only comments created at/after |
| `until` | RFC3339 timestamp | only comments created at/before |
| `limit` | int (default 50, max 200) | page size |
| `cursor` | opaque string | pagination |

Return shape: same comment objects `list_comments` returns today, plus `ticket_id` + `ticket_title` per entry (so callers don't have to join). Sorted by `created_at` ASC by default; `order=desc` to flip.

### Naming

Two options:
- **`list_comments_scoped`** (new tool) — leaves the existing `list_comments(ticket_id)` alone for backwards compat.
- **Widen `list_comments`** — make `ticket_id` optional and accept the broader filter set. Cleaner but breaks the "one tool one mental model" minimalism the README leans into.

Recommend the new-tool path — keeps existing callers stable.

### Why it matters for LLM ergonomics

The MCP is LLM-first. The current workflow above is fine for a Python script but a model burns context on:
- A 100 KB search dump
- Reading the dump back via a file-slice helper
- A Python sub-call to filter

A targeted `list_comments_scoped(project=…, exclude_author_id=<my-id>, since=<phase-creation-ts>)` returns the 6 comments directly. Single round-trip.

## Acceptance

- `list_comments_scoped(project_id_or_slug="serves", author_name="Web UI", exclude_system=true)` returns only the 6 operator-authored comments from this session
- Same call with `phase_id_or_slug="security-hardening"` returns the same set (since they all happened to be in that phase)
- Pagination via `cursor` works
- Existing `list_comments(ticket_id)` is unchanged
- README updated to describe the tool + recommend it for "find operator feedback on my recent work" workflows

## Out of scope

- Comment editing (comments stay immutable)
- Comment search semantics — that's `search_comments`'s job; this tool is purely filter+list
- Cross-project comment search (one project scope per call; the operator can loop)

## References

- Originating workflow: serves security audit follow-up (2026-05-22). 35 newly-filed tickets + 6 operator comments in 30 min; current tool surface required `search_comments` + Python filter to surface them.
- Existing tools: `list_comments(ticket_id)`, `search_comments(query, project_id_or_slug?, ticket_id?, limit?)`.
