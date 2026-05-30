// Package tickets hosts the templ-rendered ticket pages (board, detail, edit,
// new) plus the small modal/form/thread partials they compose. It mirrors the
// legacy templates/pages/tickets + templates/partials/{ticket_card,comment,
// comments_thread,comment_form,move_form,complete_form,frozen_actions,
// assign_phase_form}.tmpl 1:1 — same class names, same hrefs, same field
// names, same dialog ids. Phase 2 will diverge; Wave 1 doesn't.
//
// The props mirror the legacy *Data structs in internal/web/handlers_*.go
// (boardData, ticketDetailData, ticketFormData, commentsThreadData). The
// mirror exists for two reasons:
//
//  1. Import cycle: web → components/pages/tickets is fine, but the reverse
//     would explode. Defining ours here means tickets/* doesn't need to
//     import web.
//  2. Phase 2 hardening: when the detail metadata block grows, only this
//     package needs to learn the new shape.
//
// The web package converts its private structs into these mirrors at the
// RenderTempl boundary (see handlers_tickets.go).
package tickets

import (
	"strconv"
	"strings"

	"tickets_please/internal/domain"
)

// --- Board ----------------------------------------------------------------

// BoardProps is the payload for the board page. Tickets are pre-bucketed by
// column so the template can range without flow control over a flat slice.
type BoardProps struct {
	Project   *domain.Project
	PhaseSlug string // current phase filter, "" = unscoped
	Phases    []*domain.Phase
	Columns   []BoardColumn
}

// BoardColumn is a single named column on the board.
type BoardColumn struct {
	Column  domain.Column
	Title   string
	Tickets []*domain.Ticket
}

// --- Ticket card ----------------------------------------------------------

// TicketCardProps wraps a ticket plus its project slug for the small card
// the board page composes (and any future "list of tickets" view).
type TicketCardProps struct {
	Ticket      *domain.Ticket
	ProjectSlug string
}

// --- Detail ---------------------------------------------------------------

// DetailProps is the payload for the ticket detail page. Mirrors
// web.ticketDetailData (kept in sync at the render boundary).
type DetailProps struct {
	Project     *domain.Project
	Phases      []*domain.Phase
	Phase       *domain.Phase // nil for phase-less tickets
	Ticket      *domain.Ticket
	Depends     []*domain.Ticket
	Blocks      []*domain.Ticket
	ProjectSlug string
	CSRF        string
	IsDone      bool
	Comments    CommentsThreadProps
}

// --- Forms (new/edit) -----------------------------------------------------

// FormProps powers both the New and Edit ticket forms (one struct, the Mode
// field switches presentation). Submitted carries whatever the user just
// typed so a validation-failure rerender preserves their work.
type FormProps struct {
	Mode      string // "new" or "edit"
	Project   *domain.Project
	Phases    []*domain.Phase
	Ticket    *domain.Ticket // nil on new
	FormError string
	Submitted FormSubmitted
	CSRF      string
}

// FormSubmitted mirrors web.ticketFormSubmitted — the raw form values the
// user submitted, re-applied to inputs when the server rejects the form so
// the user doesn't lose their work.
type FormSubmitted struct {
	Title          string
	Body           string
	PhaseSlug      string
	Wave           int
	DependsOn      []string
	Parallelizable []string
}

// --- Comments thread ------------------------------------------------------

// CommentsThreadProps mirrors web.commentsThreadData — the full thread plus
// the form fields a fresh comment submits.
type CommentsThreadProps struct {
	TicketID    string
	ProjectSlug string
	CSRF        string
	Rows        []CommentRowProps
	FormError   string
}

// CommentRowProps mirrors web.commentRowData.
type CommentRowProps struct {
	Comment     *domain.Comment
	IsSystem    bool
	AuthorLabel string
}

// --- Assign-phase modal ---------------------------------------------------

// AssignPhaseProps is the payload for the reassign-phase form. Replaces the
// legacy mkAssignPhase template func.
type AssignPhaseProps struct {
	TicketID         string
	ProjectSlug      string
	CurrentPhaseSlug string
	Phases           []*domain.Phase
	CSRF             string
}

// --- helpers used by the .templ files -------------------------------------

// shortID returns the first 8 chars of an id (or the whole id if shorter).
// Used for the breadcrumb "id-pill" badge on the detail page.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// initial returns the first character of s, or "?" for empty strings. Used
// by the comment row to seed the author avatar's data-initial.
func initial(s string) string {
	if s == "" {
		return "?"
	}
	return s[:1]
}

// colText is a small helper that returns the string form of a *Column, or ""
// when nil. Mirrors the legacy `derefCol` template func.
func colText(c *domain.Column) string {
	if c == nil {
		return ""
	}
	return string(*c)
}

// transition formats the "from → to" arrow used on system_move comments.
// Built as one Go string so the literal U+2192 ARROW lands in the rendered
// HTML with the exact `from → to` spacing the tests assert on.
func transition(from, to *domain.Column) string {
	return colText(from) + " → " + colText(to)
}

// joinIDs joins ticket-id slices into the comma-separated value the inline
// "depends_on" / "parallelizable_with" inputs expect on rerender.
func joinIDs(ids []string) string {
	return strings.Join(ids, ", ")
}

// phaseSlugFor returns the slug of the ticket's current phase, or "" if it's
// phase-less or the phase isn't in the slice.
func phaseSlugFor(t *domain.Ticket, phases []*domain.Phase) string {
	if t == nil || t.PhaseID == nil {
		return ""
	}
	for _, p := range phases {
		if p.ID == *t.PhaseID {
			return p.Slug
		}
	}
	return ""
}

// formatWave renders the wave integer for the form input. Trivial wrapper to
// keep the templ call site free of strconv.
func formatWave(w int) string { return strconv.Itoa(w) }

// columnString is the string form of a Column — templ can't `printf %s` so
// the conversion lives here.
func columnString(c domain.Column) string { return string(c) }

// blockedByList renders the title attribute of a "blocked" badge — comma-
// separated list of blocker ids.
func blockedByList(ids []string) string { return strings.Join(ids, ", ") }

// ticketCardHref builds the /tickets/{id}[?slug=…] URL the card links to.
// The slug query is omitted when empty so off-board renderings (e.g. search
// results) still produce a clean URL.
func ticketCardHref(id, slug string) string {
	if slug == "" {
		return "/tickets/" + id
	}
	return "/tickets/" + id + "?slug=" + slug
}

// ticketActionHref builds /tickets/{id}/{action}?slug={slug}. Used by the
// move/complete/assign-phase/edit/delete forms whose URLs share that shape.
func ticketActionHref(id, action, slug string) string {
	base := "/tickets/" + id + "/" + action
	if slug == "" {
		return base
	}
	return base + "?slug=" + slug
}

// ticketDetailHref is the bare detail URL with optional slug hint. Mirrors
// ticketCardHref but kept separate for call-site clarity.
func ticketDetailHref(id, slug string) string {
	return ticketCardHref(id, slug)
}

// commentsHref is the /tickets/{id}/comments URL the comment form posts to.
func commentsHref(id, slug string) string {
	return ticketActionHref(id, "comments", slug)
}

// assignPhaseHref is the /tickets/{id}/assign-phase URL the modal form posts
// to.
func assignPhaseHref(id, slug string) string {
	return ticketActionHref(id, "assign-phase", slug)
}

// deleteHref is the /tickets/{id}/delete URL the delete modal posts to.
func deleteHref(id, slug string) string {
	return ticketActionHref(id, "delete", slug)
}

// editHref is the /tickets/{id}/edit URL the "Edit ticket" anchor points at.
func editHref(id, slug string) string {
	return ticketActionHref(id, "edit", slug)
}

// updateHref is the /tickets/{id}?slug=… URL the edit form posts to (no
// /edit suffix — the POST is the same handler the GET maps to via method
// dispatch on the route).
func updateHref(id, slug string) string {
	return ticketCardHref(id, slug)
}

// createHref is the /p/{slug}/tickets URL the new-ticket form posts to.
func createHref(slug string) string { return "/p/" + slug + "/tickets" }

// boardHref is the /p/{slug}/board URL — used by the board filter form's
// action and by the "Cancel" link on the new-ticket form.
func boardHref(slug string) string { return "/p/" + slug + "/board" }

// projectHref is /p/{slug} — the overview URL used as the back-link on the
// edit form.
func projectHref(slug string) string { return "/p/" + slug }

// phaseHref is /p/{slug}/phases/{phase} — the breadcrumb link on the
// detail page when the ticket has a phase.
func phaseHref(projectSlug, phaseSlug string) string {
	return "/p/" + projectSlug + "/phases/" + phaseSlug
}

// moveHref is /tickets/{id}/move?slug=… — the move-modal form's action.
func moveHref(id, slug string) string { return ticketActionHref(id, "move", slug) }

// completeHref is /tickets/{id}/complete?slug=… — the complete-modal form's
// action.
func completeHref(id, slug string) string { return ticketActionHref(id, "complete", slug) }
