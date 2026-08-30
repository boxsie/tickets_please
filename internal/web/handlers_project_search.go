package web

import (
	"errors"
	"net/http"
	"net/url"
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
	ShowArchived bool
	ToggleHref   string
	CSRF         string
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

	showArchived := a.resolveShowArchived(w, r)
	body := projectSearchData{
		Project:      proj,
		Query:        q,
		Kind:         kind,
		Limit:        limit,
		ShowArchived: showArchived,
		ToggleHref:   archivedToggleHref(r, showArchived),
		CSRF:         a.summaryCSRF(r),
	}

	if q != "" {
		body = a.runProjectSearch(r, body)
	}

	props := searchToProps(body, a.hitFeedbackCounts(r, proj.Slug, body))
	if r.Header.Get("HX-Request") == "true" {
		a.renderer.RenderTemplPartial(w, r, projectspg.SearchResults(props))
		return
	}

	a.renderer.RenderTempl(w, r, PageOpts{
		Title:       "Search · " + proj.Name + " · tickets_please",
		CurrentSlug: proj.Slug,
	}, projectspg.Search(props))
}

// handleSearchRate serves POST /p/{slug}/search/rate — a human 👍/👎 on one
// search hit, wrapping svc.RateSearchResult. On htmx it swaps the hit's rating
// widget for the sticky "rated" variant with the updated counts; a no-JS POST
// falls back to a flash + redirect to the search page.
func (a *app) handleSearchRate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	entryKey := strings.TrimSpace(r.Form.Get("entry_key"))
	rating := strings.TrimSpace(r.Form.Get("rating"))
	reason := strings.TrimSpace(r.Form.Get("reason"))
	query := r.Form.Get("query")

	if entryKey == "" || (rating != string(domain.RatingLike) && rating != string(domain.RatingDislike)) {
		a.renderer.RenderTemplError(w, r, http.StatusBadRequest,
			errors.New("entry_key and a rating of 'like' or 'dislike' are required"))
		return
	}

	out, err := a.deps.Service.RateSearchResult(r.Context(), svc.RateInput{
		ProjectIDOrSlug: slug,
		EntryKeys:       []domain.EntryKey{domain.EntryKey(entryKey)},
		Rating:          domain.Rating(rating),
		Reason:          reason,
	})
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	// Per-key partial success: a rejected key (unknown/malformed entry) means
	// the rating didn't land — surface it so the user knows it didn't take.
	if len(out.Rejected) > 0 {
		a.renderer.RenderTemplError(w, r, http.StatusUnprocessableEntity, errors.New(out.Rejected[0].Error))
		return
	}

	likes, dislikes := 0, 0
	if len(out.Updated) > 0 {
		likes, dislikes = out.Updated[0].Likes, out.Updated[0].Dislikes
	}
	widget := projectspg.RatingWidget(projectspg.RatingProps{
		Slug:     slug,
		EntryKey: entryKey,
		Likes:    likes,
		Dislikes: dislikes,
		CSRF:     a.summaryCSRF(r),
		Query:    query,
		Rated:    true,
		RatedAs:  rating,
	})
	if r.Header.Get("HX-Request") == "true" {
		a.renderer.RenderTemplPartial(w, r, widget)
		return
	}
	loc := "/p/" + slug + "/search"
	if query != "" {
		loc += "?q=" + url.QueryEscape(query)
	}
	SetFlash(w, r, "success", "Thanks — feedback noted.")
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

// searchToProps converts the web-package's projectSearchData into the
// projects-package mirror. svc.* hit types come over field-by-field so the
// templ page never imports svc.
func searchToProps(d projectSearchData, counts map[domain.EntryKey]domain.FeedbackRecord) projectspg.SearchProps {
	out := projectspg.SearchProps{
		Project:      d.Project,
		Query:        d.Query,
		Kind:         d.Kind,
		Limit:        d.Limit,
		Err:          d.Err,
		ShowArchived: d.ShowArchived,
		ToggleHref:   d.ToggleHref,
		CSRF:         d.CSRF,
	}
	out.TicketHits = make([]projectspg.TicketHit, len(d.TicketHits))
	for i, h := range d.TicketHits {
		rec := counts[h.EntryKey]
		out.TicketHits[i] = projectspg.TicketHit{
			Ticket: h.Ticket, Score: h.Score,
			EntryKey: string(h.EntryKey), Likes: rec.Likes, Dislikes: rec.Dislikes,
		}
	}
	out.CommentHits = make([]projectspg.CommentHit, len(d.CommentHits))
	for i, h := range d.CommentHits {
		rec := counts[h.EntryKey]
		out.CommentHits[i] = projectspg.CommentHit{
			Comment:     h.Comment,
			TicketTitle: h.TicketTitle,
			Score:       h.Score,
			EntryKey:    string(h.EntryKey), Likes: rec.Likes, Dislikes: rec.Dislikes,
		}
	}
	out.LearningHits = make([]projectspg.LearningHit, len(d.LearningHits))
	for i, h := range d.LearningHits {
		rec := counts[h.EntryKey]
		out.LearningHits[i] = projectspg.LearningHit{
			TicketID:    h.TicketID,
			Title:       h.Title,
			Learnings:   h.Learnings,
			Score:       h.Score,
			CompletedAt: h.CompletedAt,
			EntryKey:    string(h.EntryKey), Likes: rec.Likes, Dislikes: rec.Dislikes,
		}
	}
	return out
}

// hitFeedbackCounts gathers the like/dislike tallies for every hit's entry key
// so the rating widget can show "👍 3 · 👎 1" on initial render. Best-effort:
// any error degrades to no counts (the widgets still render their buttons).
func (a *app) hitFeedbackCounts(r *http.Request, slug string, d projectSearchData) map[domain.EntryKey]domain.FeedbackRecord {
	keys := make([]domain.EntryKey, 0, len(d.TicketHits)+len(d.CommentHits)+len(d.LearningHits))
	for _, h := range d.TicketHits {
		keys = append(keys, h.EntryKey)
	}
	for _, h := range d.CommentHits {
		keys = append(keys, h.EntryKey)
	}
	for _, h := range d.LearningHits {
		keys = append(keys, h.EntryKey)
	}
	if len(keys) == 0 {
		return nil
	}
	counts, err := a.deps.Service.FeedbackCounts(r.Context(), slug, keys)
	if err != nil {
		a.deps.Logger.Warn("search: feedback counts", "err", err)
		return nil
	}
	return counts
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
			IncludeArchived: body.ShowArchived,
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
			IncludeArchived: body.ShowArchived,
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
			IncludeArchived: body.ShowArchived,
		})
		if err != nil {
			body.Err = err.Error()
			break
		}
		body.LearningHits = hits
	}
	return body
}
