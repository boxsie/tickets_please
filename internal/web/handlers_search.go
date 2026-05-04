package web

import (
	"net/http"
	"strconv"
	"strings"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
)

// Cross-project search dispatcher. One GET /search route fans out to one of
// four Service.Search* methods depending on the kind param. Renders a full
// page on a normal nav and just the results fragment on HX-Request so the
// htmx live-search box can swap into #results without redrawing chrome.
//
// kind is one of projects | tickets | learnings | comments. Default
// learnings — the most useful single feature for power users.
//
// slug optionally restricts the search to one mounted project. Empty =
// cross-project where Service supports it.
//
// limit defaults to 20 and is clamped to Service's 50 ceiling.

const (
	searchDefaultLimit = 20
	searchMaxLimit     = 50
)

// searchPageData is the payload for pages/search.tmpl. Results is one of the
// four hit slices, picked by Kind; the template dispatches on Kind to render
// the right partial.
type searchPageData struct {
	Query        string
	Kind         string
	Slug         string
	Limit        int
	MountedCount int
	// Exactly one of the four is non-nil per request; the others are nil so
	// the template can `if .Body.LearningHits` without a separate "kind"
	// switch.
	ProjectHits  []svc.ProjectHit
	TicketHits   []svc.TicketHit
	LearningHits []svc.LearningHit
	CommentHits  []svc.CommentHit
	Err          string
}

func (a *app) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = "learnings"
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}

	mounted, err := a.deps.Service.ListProjects(r.Context())
	if err != nil {
		a.deps.Logger.Warn("search: list projects", "err", err)
		mounted = nil
	}

	body := searchPageData{
		Query:        q,
		Kind:         kind,
		Slug:         slug,
		Limit:        limit,
		MountedCount: len(mounted),
	}

	if q != "" {
		body = a.runSearch(r, body)
	}

	// HX-Request: caller is the live-search input; return just the results
	// partial so the chrome doesn't redraw on every keystroke.
	if r.Header.Get("HX-Request") == "true" {
		a.renderer.Partial(w, r, "search_results", body)
		return
	}

	a.renderer.Page(w, r, "search", PageOpts{
		Title:       "Search · tickets_please",
		CurrentSlug: slug,
		Body:        body,
	})
}

// runSearch dispatches to the right Service method by kind. Errors are
// captured into body.Err so the template can render them inline next to the
// (empty) results — better UX than a 500 page for a typo'd slug.
func (a *app) runSearch(r *http.Request, body searchPageData) searchPageData {
	switch body.Kind {
	case "projects":
		hits, err := a.deps.Service.SearchProjects(r.Context(), body.Query, body.Limit)
		if err != nil {
			body.Err = err.Error()
			break
		}
		body.ProjectHits = hits
	case "tickets":
		// SearchTickets requires a project filter — surface that constraint
		// in the inline error rather than letting Service's "v1" message
		// bubble up unfiltered.
		if body.Slug == "" {
			body.Err = "Ticket search needs a project — pick one from the slug filter."
			break
		}
		hits, err := a.deps.Service.SearchTickets(r.Context(), domain.SearchTicketsInput{
			Query:           body.Query,
			ProjectIDOrSlug: body.Slug,
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
			ProjectIDOrSlug: body.Slug,
			Limit:           body.Limit,
		})
		if err != nil {
			body.Err = err.Error()
			break
		}
		body.CommentHits = hits
	default: // "learnings"
		body.Kind = "learnings"
		hits, err := a.deps.Service.SearchLearnings(r.Context(), domain.SearchLearningsInput{
			Query:           body.Query,
			ProjectIDOrSlug: body.Slug,
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
