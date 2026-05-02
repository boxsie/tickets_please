package mcptools

import (
	"errors"
	"strings"
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
)

// formatProject converts a *domain.Project into a snake_case map ready for
// JSON marshalling as an MCP tool result. Returns nil for nil input.
func formatProject(p *domain.Project) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id":          p.ID,
		"slug":        p.Slug,
		"name":        p.Name,
		"description": p.Description,
		"summary":     p.Summary,
		"created_by":  formatAgentRef(p.CreatedBy),
		"created_at":  formatTime(p.CreatedAt),
		"updated_at":  formatTime(p.UpdatedAt),
	}
}

// formatTicket renders a *domain.Ticket as a snake_case map. Columns become
// the underlying string ("todo", "in_progress", …); slices are nil-safe.
func formatTicket(t *domain.Ticket) map[string]any {
	if t == nil {
		return nil
	}
	out := map[string]any{
		"id":                  t.ID,
		"project_id":          t.ProjectID,
		"title":               t.Title,
		"body":                t.Body,
		"column":              string(t.Column),
		"phase_id":            ptrStringOrNil(t.PhaseID),
		"wave":                t.Wave,
		"depends_on":          stringSlice(t.DependsOn),
		"parallelizable_with": stringSlice(t.ParallelizableWith),
		"blocked_by":          stringSlice(t.BlockedBy),
		"created_by":          formatAgentRef(t.CreatedBy),
		"completed_by":        formatAgentRef(t.CompletedBy),
		"completed_at":        formatTimePtr(t.CompletedAt),
		"created_at":          formatTime(t.CreatedAt),
		"updated_at":          formatTime(t.UpdatedAt),
		"testing_evidence":    ptrStringOrNil(t.TestingEvidence),
		"work_summary":        ptrStringOrNil(t.WorkSummary),
		"learnings":           ptrStringOrNil(t.Learnings),
	}
	return out
}

// formatComment renders a *domain.Comment. FromColumn / ToColumn become
// strings (omitted when nil so non-move comments don't carry meaningless
// "from": "" entries).
func formatComment(c *domain.Comment) map[string]any {
	if c == nil {
		return nil
	}
	out := map[string]any{
		"id":         c.ID,
		"ticket_id":  c.TicketID,
		"kind":       string(c.Kind),
		"body":       c.Body,
		"author":     formatAgentRef(c.Author),
		"created_at": formatTime(c.CreatedAt),
	}
	if c.FromColumn != nil {
		out["from_column"] = string(*c.FromColumn)
	} else {
		out["from_column"] = nil
	}
	if c.ToColumn != nil {
		out["to_column"] = string(*c.ToColumn)
	} else {
		out["to_column"] = nil
	}
	return out
}

// formatPhase renders a *domain.Phase including its computed ticket counts.
func formatPhase(p *domain.Phase) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id":                  p.ID,
		"project_id":          p.ProjectID,
		"slug":                p.Slug,
		"name":                p.Name,
		"description":         p.Description,
		"summary":             p.Summary,
		"number":              p.Number,
		"created_by":          formatAgentRef(p.CreatedBy),
		"created_at":          formatTime(p.CreatedAt),
		"updated_at":          formatTime(p.UpdatedAt),
		"ticket_count":        p.TicketCount,
		"active_ticket_count": p.ActiveTicketCount,
	}
}

// formatAgent renders a full *domain.Agent (used by who_am_i indirectly via
// GetAgent if ever needed). Metadata is passed through verbatim.
func formatAgent(a *domain.Agent) map[string]any {
	if a == nil {
		return nil
	}
	meta := map[string]any{}
	for k, v := range a.Metadata {
		meta[k] = v
	}
	return map[string]any{
		"id":           a.ID,
		"key":          a.Key,
		"name":         a.Name,
		"metadata":     meta,
		"created_at":   formatTime(a.CreatedAt),
		"expires_at":   formatTime(a.ExpiresAt),
		"last_seen_at": formatTime(a.LastSeenAt),
	}
}

// formatProjectHit wraps a search.ProjectHit with score + project shape.
func formatProjectHit(h svc.ProjectHit) map[string]any {
	return map[string]any{
		"project": formatProject(h.Project),
		"score":   h.Score,
	}
}

// formatTicketHit wraps a search.TicketHit.
func formatTicketHit(h svc.TicketHit) map[string]any {
	return map[string]any{
		"ticket": formatTicket(h.Ticket),
		"score":  h.Score,
	}
}

// formatCommentHit wraps a search.CommentHit, including the denormalised
// ticket title for cheap rendering.
func formatCommentHit(h svc.CommentHit) map[string]any {
	return map[string]any{
		"comment":      formatComment(h.Comment),
		"score":        h.Score,
		"ticket_title": h.TicketTitle,
	}
}

// formatLearningHit renders a search.LearningHit.
func formatLearningHit(h svc.LearningHit) map[string]any {
	return map[string]any{
		"ticket_id":    h.TicketID,
		"project_id":   h.ProjectID,
		"title":        h.Title,
		"learnings":    h.Learnings,
		"score":        h.Score,
		"completed_at": formatTime(h.CompletedAt),
	}
}

// formatWaveSummary renders a domain.WaveSummary.
func formatWaveSummary(w domain.WaveSummary) map[string]any {
	return map[string]any{
		"wave":                w.Wave,
		"ticket_count":        w.TicketCount,
		"active_ticket_count": w.ActiveTicketCount,
	}
}

// formatAgentRef renders a *domain.AgentRef as the {"id": ..., "name": ...}
// shape SPEC describes; nil ref → nil so the JSON carries `null` rather than
// an empty object.
func formatAgentRef(a *domain.AgentRef) any {
	if a == nil {
		return nil
	}
	return map[string]any{
		"id":   a.ID,
		"name": a.Name,
	}
}

// formatTime returns an RFC3339 string for non-zero times, or nil for the
// zero time so JSON readers can distinguish "no value" from epoch.
func formatTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// formatTimePtr returns nil when the pointer is nil, else the RFC3339 string.
func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// ptrStringOrNil returns nil when the pointer is nil, else the dereferenced
// string. Used so JSON readers see `null` for unset fields instead of "".
func ptrStringOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// stringSlice returns []string{} for nil input so JSON encoders emit `[]`
// instead of `null` — easier for LLMs to iterate without a nil check.
func stringSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// formatError translates a sentinel error from the domain package into the
// "<prefix>: <message>" form the SPEC pins for MCP error reporting. Any
// non-sentinel error falls through with no prefix so unexpected failures
// surface their raw message.
//
// "<message>" is the wrapped error's text with the sentinel string stripped
// off the front (errors are constructed as `fmt.Errorf("%w: <msg>", sentinel)`
// throughout svc, so the leading sentinel text + ": " is redundant once the
// caller knows the prefix).
func formatError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, domain.ErrInvalidArgument):
		return "invalid argument: " + stripSentinel(err, domain.ErrInvalidArgument)
	case errors.Is(err, domain.ErrNotFound):
		return "not found: " + stripSentinel(err, domain.ErrNotFound)
	case errors.Is(err, domain.ErrAlreadyExists):
		return "already exists: " + stripSentinel(err, domain.ErrAlreadyExists)
	case errors.Is(err, domain.ErrFailedPrecondition):
		return "precondition failed: " + stripSentinel(err, domain.ErrFailedPrecondition)
	case errors.Is(err, domain.ErrUnauthenticated):
		return "unauthenticated; re-registering..."
	default:
		return err.Error()
	}
}

// stripSentinel returns err.Error() with a leading "<sentinel>: " removed if
// present. svc constructs errors as `fmt.Errorf("%w: <msg>", sentinel)`, so
// "<sentinel>: " is the predictable redundant prefix.
func stripSentinel(err, sentinel error) string {
	full := err.Error()
	prefix := sentinel.Error() + ": "
	if strings.HasPrefix(full, prefix) {
		return full[len(prefix):]
	}
	return full
}
