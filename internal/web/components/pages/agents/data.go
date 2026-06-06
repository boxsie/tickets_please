// Package agents hosts the templ pages for the app-global agents screens:
// the registry index (/agents) and the per-agent activity detail (/agents/{id}).
//
// As with the other page packages, the prop types here are structural mirrors
// the handler fills at the render boundary — kept local so the components don't
// import web/svc (which would cycle). They depend only on domain + reltime.
package agents

import (
	"strconv"
	"strings"
	"time"

	"tickets_please/internal/domain"
)

// IndexProps drives the /agents table.
type IndexProps struct {
	Agents []AgentRow
}

// AgentRow is one registered agent as the index table renders it. The handler
// flattens domain.Agent (+ its metadata bag) into this display shape; the
// template stays declarative.
type AgentRow struct {
	ID    string
	Name  string
	Model string
	// Client is the registering client (e.g. "Claude Code") — shown as the
	// muted second line under Model since there's no separate provider field.
	Client string
	Key    string
	// ActingForUserID / ActingForName name the human an acting-for agent is
	// bound to. Empty for a plain key-only agent. Rendered as plain text for
	// now — the clickable /u/{user} link is a later ticket (the route doesn't
	// exist yet, so we don't link to a 404).
	ActingForUserID string
	ActingForName   string
	Registered      time.Time
	LastSeen        time.Time
	// NeverSeen is true when LastSeenAt was zero — render "never" instead of a
	// bogus relative time off the zero instant.
	NeverSeen        bool
	TicketsCreated   int
	TicketsCompleted int
	CommentsAuthored int
}

// AgentRowFrom flattens a hydrated domain.Agent into the display row. Kept here
// (not in the handler) so the index and detail pages share one mapping.
func AgentRowFrom(a *domain.Agent) AgentRow {
	row := AgentRow{
		ID:               a.ID,
		Name:             a.Name,
		Model:            metaModel(a),
		Client:           a.Metadata["client_name"],
		Key:              a.Key,
		Registered:       a.CreatedAt,
		LastSeen:         a.LastSeenAt,
		NeverSeen:        a.LastSeenAt.IsZero(),
		TicketsCreated:   a.TicketsCreated,
		TicketsCompleted: a.TicketsCompleted,
		CommentsAuthored: a.CommentsAuthored,
	}
	if a.ActingFor != nil {
		row.ActingForUserID = a.ActingFor.UserID
		row.ActingForName = a.ActingFor.DisplayName
		if row.ActingForName == "" {
			row.ActingForName = a.ActingFor.UserID
		}
	}
	return row
}

// metaModel joins model + model_version into a single label, falling back to a
// dash when the agent registered without a model claim.
func metaModel(a *domain.Agent) string {
	model := a.Metadata["model"]
	if v := a.Metadata["model_version"]; v != "" {
		if model != "" {
			return model + " · " + v
		}
		return v
	}
	if model == "" {
		return "—"
	}
	return model
}

// agentHref is the detail-page link for an agent row.
func agentHref(id string) string { return "/agents/" + id }

// keyShort truncates a long agent key for the table cell; the full value rides
// on the copy button's data-copy so nothing is lost.
func keyShort(key string) string {
	const max = 18
	if len(key) <= max {
		return key
	}
	return key[:max-1] + "…"
}

// filterKey is the lowercased haystack the client-side filter matches against —
// name, model, key, and client, space-joined.
func filterKey(a AgentRow) string {
	return strings.ToLower(strings.Join([]string{a.Name, a.Model, a.Key, a.Client}, " "))
}

// sortStamp renders a time as a sortable numeric string (unix nanos). A
// never-seen agent collapses to "0" so it sorts to the bottom under last-seen
// desc, matching the server-side ordering.
func sortStamp(t time.Time, never bool) string {
	if never || t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}
