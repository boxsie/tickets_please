package web

import (
	"net/http"
	"strings"

	"tickets_please/internal/domain"
	pgtickets "tickets_please/internal/web/components/pages/tickets"
	"tickets_please/internal/web/components/partials"
)

// Comments handlers — the immutable audit trail UI on the ticket detail
// page. Two routes:
//
//   - GET /tickets/{id}/comments — htmx fragment refresh.
//   - POST /tickets/{id}/comments — append a new comment; returns the new
//     comment's partial so htmx can hx-swap="beforeend" it onto the thread
//     without a full reload.
//
// Comments are immutable per SPEC §Design decisions; the UI never offers
// edit or delete affordances. System comments (system_move, system_completion)
// render with a distinguishable badge — humans should be able to tell them
// apart from authored notes at a glance.

// commentRowData is the payload for partials/comment.tmpl.
type commentRowData struct {
	Comment     *domain.Comment
	IsSystem    bool
	AuthorLabel string
}

// commentsThreadData is the payload for partials/comments_thread.tmpl and
// pages/tickets/detail.tmpl's #comments slot.
type commentsThreadData struct {
	TicketID    string
	ProjectSlug string
	CSRF        string
	Rows        []commentRowData
	FormError   string
}

// handleCommentsList: GET /tickets/{id}/comments — refresh fragment.
func (a *app) handleCommentsList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	comments, err := a.deps.Service.ListComments(r.Context(), id)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	body := pgtickets.CommentsThreadProps{
		TicketID:    id,
		ProjectSlug: r.URL.Query().Get("slug"),
		CSRF:        a.summaryCSRF(r),
		Rows:        commentRowsTempl(comments),
	}
	a.renderer.RenderTemplPartial(w, r, pgtickets.CommentsThread(body))
}

// handleCommentCreate: POST /tickets/{id}/comments — append.
func (a *app) handleCommentCreate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body := strings.TrimSpace(r.Form.Get("body"))
	created, err := a.deps.Service.CreateComment(r.Context(), id, body)
	if err != nil {
		// HX-Request: return the inline error fragment so the form can swap
		// it next to itself; non-HX falls through to the generic error page.
		if r.Header.Get("HX-Request") == "true" {
			status := classifyServiceError(err)
			w.WriteHeader(status)
			a.renderer.RenderTemplPartial(w, r, partials.Error(partials.ErrorProps{
				Status:  status,
				Message: err.Error(),
			}))
			return
		}
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	row := buildCommentRow(created)
	if r.Header.Get("HX-Request") == "true" {
		// Render the single new comment so htmx can append it; the form
		// resets client-side via hx-on::after-request.
		a.renderer.RenderTemplPartial(w, r, pgtickets.CommentRow(toTemplRow(row)))
		return
	}
	slug := r.URL.Query().Get("slug")
	loc := "/tickets/" + id
	if slug != "" {
		loc += "?slug=" + slug
	}
	SetFlash(w, r, "success", "Comment added.")
	http.Redirect(w, r, loc+"#comment-"+created.ID, http.StatusSeeOther)
}

// commentRows turns a chronologically-ordered slice of comments into render
// rows, computing the system badge + author label per row.
func commentRows(comments []*domain.Comment) []commentRowData {
	out := make([]commentRowData, 0, len(comments))
	for _, c := range comments {
		out = append(out, buildCommentRow(c))
	}
	return out
}

func buildCommentRow(c *domain.Comment) commentRowData {
	label := "unknown agent"
	if c.Author != nil && c.Author.Name != "" {
		label = c.Author.Name
	} else if c.Author != nil && c.Author.ID != "" {
		// Fall back to an id stub so the audit trail still references the
		// agent — beats showing "unknown" when the agent record is gone.
		label = c.Author.ID[:min(8, len(c.Author.ID))]
	}
	// "Claude (for Dan)" when the authoring agent was acting on behalf of a
	// registered user. The clickable user-page / agent-page links are a
	// Phase-2 W3 deliverable (those routes don't exist yet), so today the
	// acting-for relationship renders as plain text.
	if c.AuthorFor != nil {
		forName := c.AuthorFor.DisplayName
		if forName == "" {
			forName = c.AuthorFor.UserID[:min(8, len(c.AuthorFor.UserID))]
		}
		label = label + " (for " + forName + ")"
	}
	return commentRowData{
		Comment:     c,
		IsSystem:    c.Kind != domain.CommentKindUser,
		AuthorLabel: label,
	}
}

// commentRowsTempl converts the chronologically-ordered comments into the
// templ-shaped row props the new tickets package consumes. Mirrors
// commentRows but emits pgtickets.CommentRowProps.
func commentRowsTempl(comments []*domain.Comment) []pgtickets.CommentRowProps {
	out := make([]pgtickets.CommentRowProps, 0, len(comments))
	for _, c := range comments {
		out = append(out, toTemplRow(buildCommentRow(c)))
	}
	return out
}

// toTemplRow converts the legacy commentRowData (used by the html/template
// renderer + still useful for the cookie-cutter buildCommentRow helper) into
// the templ-shaped pgtickets.CommentRowProps.
func toTemplRow(row commentRowData) pgtickets.CommentRowProps {
	return pgtickets.CommentRowProps{
		Comment:     row.Comment,
		IsSystem:    row.IsSystem,
		AuthorLabel: row.AuthorLabel,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
