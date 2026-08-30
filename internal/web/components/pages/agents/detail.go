package agents

import (
	"time"

	pgtickets "tickets_please/internal/web/components/pages/tickets"
)

// DetailProps drives the /agents/{id} page: the agent header + counters, the
// "currently working on" callout, and one page of the activity feed.
type DetailProps struct {
	Agent       AgentRow
	CurrentWork []pgtickets.TicketCardProps
	Activity    []ActivityEntry
	// Page is the zero-based page index. PrevHref / NextHref are empty when
	// there is no previous / next page (the template hides the control).
	Page     int
	PrevHref string
	NextHref string
}

// ActivityEntry is one row in the timeline, already mapped to the native
// partial it renders through. Exactly one of Card / Comment is set for the
// ticket and comment kinds; agent_registered carries neither (just the verb +
// timestamp).
type ActivityEntry struct {
	Kind        string // ticket_created | ticket_completed | comment_added | agent_registered
	At          time.Time
	ProjectSlug string
	// Card is set for ticket_created / ticket_completed.
	Card *pgtickets.TicketCardProps
	// Comment + Ticket{ID,Title} are set for comment_added (the commented
	// ticket gives the row its "on <ticket>" link + context).
	Comment     *pgtickets.CommentRowProps
	TicketID    string
	TicketTitle string
}

// activityIcon maps an activity kind to its leading glyph.
func activityIcon(kind string) string {
	switch kind {
	case "ticket_created":
		return "✦"
	case "ticket_completed":
		return "✓"
	case "comment_added":
		return "💬"
	case "agent_registered":
		return "⚑"
	default:
		return "•"
	}
}

// activityVerb is the human label for an activity kind.
func activityVerb(kind string) string {
	switch kind {
	case "ticket_created":
		return "created a ticket"
	case "ticket_completed":
		return "completed a ticket"
	case "comment_added":
		return "commented"
	case "agent_registered":
		return "registered with the server"
	default:
		return "did something"
	}
}

// ticketHref is the link to a ticket from a comment activity row.
func ticketHref(id string) string { return "/tickets/" + id }
