## Testing evidence
go test ./internal/mcptools/... ./internal/svc/... all green; focused complete_ticket tests confirm relaxed schema. Full work writeup in follow-up comment.

## Work summary
Relaxed complete_ticket so only learnings is required. tools.go drops Required on te/ws; svc/tickets.go drops the te/ws min-length and omits empty sections from completion.md. Tests + docs updated. Full writeup in follow-up comment.

## Learnings
Only learnings earns its place across tickets via search_learnings; te/ws are within-ticket audit trail and were causing padding pressure on trivial tickets. Hit the Claude Code MCP envelope bug while completing this very ticket — long payload was rejected with the misleading required-argument error, so falling back to minimal completion plus follow-up comment per the global CLAUDE.md guidance. Full rich-text learnings in the follow-up comment.</learnings>
</invoke>
