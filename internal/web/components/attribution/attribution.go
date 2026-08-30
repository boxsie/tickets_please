// Package attribution renders the small "author · relative-time" chip shown
// beside every ticket (and comment) reference across the UI — cards, list
// rows, wave rows, search hits, dependency links. Centralising it keeps the
// author-resolution rule and the markup identical everywhere.
//
// It depends only on domain + reltime, so any page package (tickets, phases,
// projects) can import it without an import cycle.
package attribution

import (
	"tickets_please/internal/domain"
)

// Label is the display name credited for a ticket: the creating agent's name,
// falling back to the human an acting-for agent stood in for, then "unknown"
// for pre-attribution data.
func Label(t *domain.Ticket) string {
	if t.CreatedBy != nil && t.CreatedBy.Name != "" {
		return t.CreatedBy.Name
	}
	if t.CreatedFor != nil && t.CreatedFor.DisplayName != "" {
		return t.CreatedFor.DisplayName
	}
	return "unknown"
}

// commentLabel is the display name credited for a comment author. System
// comments (move/transition) carry no agent and fall back to "system".
func commentLabel(c *domain.Comment) string {
	if c.Author != nil && c.Author.Name != "" {
		return c.Author.Name
	}
	if c.AuthorFor != nil && c.AuthorFor.DisplayName != "" {
		return c.AuthorFor.DisplayName
	}
	return "system"
}
