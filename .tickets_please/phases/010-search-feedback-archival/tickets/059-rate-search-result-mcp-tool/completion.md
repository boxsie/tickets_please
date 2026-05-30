## Testing evidence
go test ./... green; rebuilt + restarted local service.

## Work summary
svc.RateSearchResult + rate_search_result MCP tool; expectedTools + totalTools bumped 31 to 32; SPEC and README updated. See ticket comment for full breakdown.

## Learnings
Handler uses defensive GetArguments to BindArguments fallback for the array envelope. See ticket comment for full breakdown.
