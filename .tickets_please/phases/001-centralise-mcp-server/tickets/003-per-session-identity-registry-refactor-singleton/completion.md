## Testing evidence
Subagent commit a612802 merged as 219b949. go build clean, go test and go test -race on internal/mcptools both clean. Five new unit tests in identity_test.go.

## Work summary
Replaced Identity singleton with per-session Registry in internal/mcptools/identity.go. Tools.identity becomes Tools.registry. callWithRetry uses ClientSessionFromContext with stdio fallback and returns ErrUnauthenticated on miss. main.go runMCP pre-registers a stdio Session under sessionID stdio.

## Learnings
Worktree subagents work cleanly when scopes are file-level separable; T002 and T003 merged with zero conflicts because only main.go was shared and they coordinated on different functions. mcp-go stdioSession.SessionID() returns the literal string stdio so the synthetic fallback matches the prod path. svc.WithSessionID expects an agent UUID, not the MCP transport session id; the registry bridges them. Brief subagents thoroughly with self-contained context up front and they finish in one round.
