## Testing evidence
Subagent commit af8ee24 merged. go build clean, go test all PASS, go test race mcptools clean, go vet clean. Manual: handler with explicit param wins over session, session fallback works when param empty, error message fires when neither set.

## Work summary
Added resolveProjectSlug helper in internal mcptools tools.go: explicit param wins, falls back to session ProjectSlug from Registry, errors with helpful message pointing to register_agent or explicit param. Dropped mcp Required from project_id_or_slug schema on 15 tools and replaced RequireString call with helper in those handlers. Updated each tool description to mention optional if register_agent has bound a project. Added MCP_PROJECT_SLUG env var read in main.go runMCP so stdio sessions can pre-bind a project.

## Learnings
Brief listed 13 affected handlers but actual count is 15 — get_phase and get_phase_summary also take project_id_or_slug as parent project parameter. search_learnings and search_comments were already optional and use empty string as scope-wide semantic so they did not need the helper. Touching them would change behavior. Stdio session can be pre-bound via MCP_PROJECT_SLUG env var without forcing register_agent for legacy clients. Tool descriptions stay terse: one extra clause not a paragraph.
