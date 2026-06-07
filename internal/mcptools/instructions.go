package mcptools

// ServerInstructions is the persistent guidance surfaced to LLM clients via the
// MCP `initialize` response. Most MCP clients (Claude Desktop, Claude Code, etc.)
// inject this into the model's context every turn so it survives across tool calls.
//
// Keep it tight. Cross-tool workflow reflexes belong here; per-tool details
// belong on the tool's own description.
const ServerInstructions = `tickets_please is a Trello-shaped, LLM-first ticketing system. Projects organize work; tickets flow through ` + "`todo` → `in_progress` → `testing` → `done`" + `. The system feeds itself: every completed ticket leaves searchable learnings for future work.

## Workflow reflexes

- **Before working in a project**, read its summary with ` + "`get_project_summary`" + `. The summary is a load-bearing context document — goals, constraints, key components — written by whoever scoped the project.
- **Before starting any non-trivial ticket**, run ` + "`search_learnings`" + ` with relevant terms. Past work may have left notes about gotchas. Skipping this is the most common avoidable mistake.
- **After any search** (` + "`search_learnings` / `search_tickets` / `search_comments`" + `), rate the hits with ` + "`rate_search_result`" + ` — each search response hands you the exact ` + "`entry_keys`" + ` in its ` + "`feedback_hint`" + `, so it's one cheap follow-up call. Ratings tune ranking so good content surfaces and stale content sinks; ` + "`dislike`" + ` a wrong hit with a ` + "`reason`" + ` that records the fix. This is the other half of the loop above — searching without rating lets the corpus rot.
- **To find unblocked work**: ` + "`list_tickets`" + ` with ` + "`ready_only=true`" + `. This filters to tickets whose ` + "`depends_on`" + ` are all done.
- **When picking up a ticket**, move it to ` + "`in_progress`" + ` with a brief comment via ` + "`move_ticket`" + ` explaining what you're starting on.
- **When done**, call ` + "`complete_ticket`" + `. Only ` + "`learnings`" + ` is required (≥10 chars) — that's the field future agents search, so write it for them. ` + "`testing_evidence`" + ` and ` + "`work_summary`" + ` are optional audit-trail fields; supply them when there's substantive content, omit on small/obvious work rather than padding.

## Hard rules (enforced server-side)

- **Every column move requires a non-empty comment.** No silent moves; the comment becomes part of the audit trail.
- **The ` + "`done`" + ` column is reachable only via ` + "`complete_ticket`" + `**, never ` + "`move_ticket`" + `. Attempts to move-to-done are rejected with an error pointing at the right tool.
- **Once a ticket is ` + "`done`" + `, it's frozen.** No reopen, no edits to completion fields. If work resurfaces, create a new ticket — past learnings still surface via search.
- **Comments are immutable** once written. Typos get a follow-up comment, not an edit.

## Optional structure

- **Phases** are sub-projects for bigger bodies of work. When entering one, read ` + "`get_phase_summary`" + ` the same way you'd read a project summary.
- **Waves** are soft integer groupings inside a phase or project. Use ` + "`list_waves`" + ` to see how a body of work decomposes; ` + "`list_tickets`" + ` accepts a ` + "`wave`" + ` filter. Waves don't gate execution — they're an organizational hint.
- **` + "`depends_on`" + `** is a hard prerequisite (gates ` + "`ready_only`" + `). **` + "`parallelizable_with`" + `** is purely advisory.

## Bootstrapping a new project

` + "`create_project`" + ` is the one mutation that does NOT require a session — it's the bootstrap escape valve. Cold-starting in a fresh repo:

1. Call ` + "`create_project`" + `. On a remote (HTTP) server, just pass ` + "`slug`, `name`, and `summary`" + ` — the server stores the project at ` + "`<remote_project_root>/<slug>`" + ` automatically. Stdio clients additionally pass ` + "`project_path`" + ` (the absolute path of the local repo) so ` + "`<project_path>/.tickets_please/project.yaml`" + ` lands inside their working tree. ` + "`created_by`" + ` is left empty when no session is registered (attribution begins with the next mutation).
2. Call ` + "`register_agent`" + ` to bind your session. Remote clients pass ` + "`project_slug`" + `; stdio clients pass ` + "`project_path`" + `.
3. All other tools work normally.

If ` + "`register_agent`" + ` reports no project at the resolved path, the project doesn't exist yet — call ` + "`create_project`" + ` first.

## Identity

Your identity travels with every mutation as ` + "`created_by` / `completed_by` / `author_id`" + `. ` + "`who_am_i`" + ` shows the registered identity if you want to confirm. HTTP clients should call ` + "`register_agent`" + ` once on connection to declare their model, client, and bound project. Stdio clients can skip if ` + "`MCP_AGENT_*`" + ` env vars are set — the binary pre-registers a session at startup.
`
