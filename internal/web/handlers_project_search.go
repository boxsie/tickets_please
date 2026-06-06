package web

import (
	"net/http"
	"strconv"
	"strings"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
	projectspg "tickets_please/internal/web/components/pages/projects"
)

// Per-project semantic search. One GET /p/{slug}/search route fans out to
// Service.SearchTickets/Comments/Learnings depending on the kind param.
// Embedding + index lookup happen in the Service layer through the per-mount
// embed.Provider + *vecindex.Index pair, so the request never crosses mounts
// (which would mean crossing embedder dimensions).
//
// kind ∈ {tickets, comments, learnings}. Default learnings — the most useful
// tab for "what did past-me figure out about X".
//
// HX-Request: true returns just the results partial so the live-search input
// can swap into #results without redrawing chrome.
//
// Empty query short-circuits to "no results" without dialling the embedder.

const (
	searchDefaultLimit = 20
	searchMaxLimit     = 50
)

// projectSearchData is the payload for pages/projects/search.tmpl. The Body
// shape is also what the search_results.tmpl partial expects so HX-Request can
// re-render just the results block.
type projectSearchData struct {
	Project      *domain.Project
	Query        string
	Kind         string
	Limit        int
	TicketHits   []svc.TicketHit
	CommentHits  []svc.CommentHit
	LearningHits []svc.LearningHit
	Err          string
}

// handleProjectSearch serves GET /p/{slug}/search. Resolves the project by
// slug (so an unknown slug 404s before we touch the embedder), runs the
// search, and renders the page (or just the results partial on HX-Request).
func (a *app) handleProjectSearch(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	switch kind {
	case "tickets", "comments", "learnings":
	default:
		kind = "learnings"
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}

	body := projectSearchData{
		Project: proj,
		Query:   q,
		Kind:    kind,
		Limit:   limit,
	}

	if q != "" {
		body = a.runProjectSearch(r, body)
	}

	props := searchToProps(body)
	if r.Header.Get("HX-Request") == "true" {
		a.renderer.RenderTemplPartial(w, r, projectspg.SearchResults(props))
		return
	}

	a.renderer.RenderTempl(w, r, PageOpts{
		Title:       "Search · " + proj.Name + " · tickets_please",
		CurrentSlug: proj.Slug,
	}, projectspg.Search(props))
}

// searchToProps converts the web-package's projectSearchData into the
// projects-package mirror. svc.* hit types come over field-by-field so the
// templ page never imports svc.
func searchToProps(d projectSearchData) projectspg.SearchProps {
	out := projectspg.SearchProps{
		Project: d.Project,
		Query:   d.Query,
		Kind:    d.Kind,
		Limit:   d.Limit,
		Err:     d.Err,
	}
	out.TicketHits = make([]projectspg.TicketHit, len(d.TicketHits))
	for i, h := range d.TicketHits {
		out.TicketHits[i] = projectspg.TicketHit{Ticket: h.Ticket, Score: h.Score}
	}
	out.CommentHits = make([]projectspg.CommentHit, len(d.CommentHits))
	for i, h := range d.CommentHits {
		out.CommentHits[i] = projectspg.CommentHit{
			Comment:     h.Comment,
			TicketTitle: h.TicketTitle,
			Score:       h.Score,
		}
	}
	out.LearningHits = make([]projectspg.LearningHit, len(d.LearningHits))
	for i, h := range d.LearningHits {
		out.LearningHits[i] = projectspg.LearningHit{
			TicketID:    h.TicketID,
			Title:       h.Title,
			Learnings:   h.Learnings,
			Score:       h.Score,
			CompletedAt: h.CompletedAt,
		}
	}
	return out
}

// runProjectSearch dispatches to the right Service method based on kind.
// Errors land in body.Err so the template can render them inline alongside
// (empty) results — easier on the user than a 500 page when their query was
// the only problem.
func (a *app) runProjectSearch(r *http.Request, body projectSearchData) projectSearchData {
	slug := body.Project.Slug
	switch body.Kind {
	case "tickets":
		hits, err := a.deps.Service.SearchTickets(r.Context(), domain.SearchTicketsInput{
			Query:           body.Query,
			ProjectIDOrSlug: slug,
			Limit:           body.Limit,
		})
		if err != nil {
			body.Err = err.Error()
			break
		}
		body.TicketHits = hits
	case "comments":
		hits, err := a.deps.Service.SearchComments(r.Context(), domain.SearchCommentsInput{
			Query:           body.Query,
			ProjectIDOrSlug: slug,
			Limit:           body.Limit,
		})
		if err != nil {
			body.Err = err.Error()
			break
		}
		body.CommentHits = hits
	default: // learnings
		body.Kind = "learnings"
		hits, err := a.deps.Service.SearchLearnings(r.Context(), domain.SearchLearningsInput{
			Query:           body.Query,
			ProjectIDOrSlug: slug,
			Limit:           body.Limit,
		})
		if err != nil {
			body.Err = err.Error()
			break
		}
		body.LearningHits = hits
	}
	return body
}
