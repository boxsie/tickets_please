package tickets

import (
	"strconv"
	"time"

	"tickets_please/internal/domain"
)

// agentName is the display label for an attribution AgentRef — the agent's
// self-reported name, or "unknown" when the ref is nil (pre-attribution data).
func agentName(a *domain.AgentRef) string {
	if a == nil || a.Name == "" {
		return "unknown"
	}
	return a.Name
}

// actingForName returns the display name of the human an acting-for agent was
// bound to when it touched the ticket. Prefers the completer (more recent)
// over the creator. Empty for plain key-only agents — the common case.
func actingForName(t *domain.Ticket) string {
	if t.CompletedFor != nil && t.CompletedFor.DisplayName != "" {
		return t.CompletedFor.DisplayName
	}
	if t.CreatedFor != nil && t.CreatedFor.DisplayName != "" {
		return t.CreatedFor.DisplayName
	}
	return ""
}

// showUpdated reports whether UpdatedAt is meaningfully later than CreatedAt
// (>1m). Below that the two stamps are effectively the create event and the
// "Updated" row is just noise.
func showUpdated(t *domain.Ticket) bool {
	return t.UpdatedAt.Sub(t.CreatedAt) > time.Minute
}

// entryKey is the search-feedback canonical id for a ticket — the string a
// human pastes into rate_search_result.
func entryKey(id string) string { return "ticket:" + id }

// countLabel renders "1 dependency" / "3 dependencies" — the singular/plural
// summary shown on the metadata-block disclosure summaries.
func countLabel(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}
