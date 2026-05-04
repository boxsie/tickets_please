package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"tickets_please/internal/domain"
)

// Tickets handlers — board, create form/post, detail, edit form/post, move,
// complete. Mirrors the projects/phases handler shape: withCSRF wraps every
// POST, classifyServiceError maps domain sentinels to HTTP statuses, and the
// renderer's Error path surfaces inline 422s when a hard rule fires.
//
// Hard rules surfaced inline:
//   - MoveTicket(target=done) — server rejects; UI omits "done" from the
//     move dropdown and routes that intent to the completion form.
//   - Move requires non-empty comment — server enforces; form has a required
//     textarea and the 422 partial renders next to it on rejection.
//   - Complete requires three textareas of ≥10 chars each — minlength on the
//     client + server-side enforcement.
//   - done tickets are frozen — detail page renders the frozen-actions panel
//     and the edit URL refuses with a 422 inline error.
//
// ?slug= hint convention: every /tickets/{id}/* URL accepts an optional
// `slug` query param used as a UX optimisation. Service.GetTicket /
// MoveTicket / CompleteTicket walk hostStoreForTicket on each call (O(active
// mounts)); a URL-side hint reserves room for a slug-direct fast path
// without changing handler logic now. Templates that link forward to
// /tickets/{id} and /tickets/{id}/{move,complete,edit,assign-phase} all
// preserve the slug hint when one is in scope.

// --- board ----------------------------------------------------------------

// boardData is the payload for pages/tickets/board.tmpl. Tickets are
// pre-bucketed by column so the template doesn't need flow control over the
// flat slice.
type boardData struct {
	Project    *domain.Project
	PhaseSlug  string         // current phase filter, or "" for unscoped
	Phases     []*domain.Phase // for the phase filter dropdown
	Columns    []boardColumn
}

type boardColumn struct {
	Column  domain.Column
	Title   string
	Tickets []*domain.Ticket
}

func (a *app) handleBoard(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	phases, err := a.deps.Service.ListPhases(r.Context(), slug)
	if err != nil {
		a.deps.Logger.Warn("board: list phases", "err", err)
		phases = nil
	}
	phaseSlug := r.URL.Query().Get("phase")
	in := domain.ListTicketsInput{
		ProjectIDOrSlug: slug,
		Limit:           200,
	}
	if phaseSlug != "" {
		in.PhaseIDOrSlug = &phaseSlug
	}
	tickets, _, err := a.deps.Service.ListTickets(r.Context(), in)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	cols := []boardColumn{
		{Column: domain.ColumnTodo, Title: "To do"},
		{Column: domain.ColumnInProgress, Title: "In progress"},
		{Column: domain.ColumnTesting, Title: "Testing"},
		{Column: domain.ColumnDone, Title: "Done"},
	}
	byCol := map[domain.Column]int{}
	for i, c := range cols {
		byCol[c.Column] = i
	}
	for _, t := range tickets {
		if i, ok := byCol[t.Column]; ok {
			cols[i].Tickets = append(cols[i].Tickets, t)
		}
	}
	a.renderer.Page(w, r, "tickets/board", PageOpts{
		Title:       "Board · " + proj.Name,
		CurrentSlug: slug,
		Body: boardData{
			Project:   proj,
			PhaseSlug: phaseSlug,
			Phases:    phases,
			Columns:   cols,
		},
	})
}

// --- create form / post ----------------------------------------------------

type ticketFormData struct {
	Mode      string // "new" or "edit"
	Project   *domain.Project
	Phases    []*domain.Phase
	Ticket    *domain.Ticket // nil on new
	FormError string
	Submitted ticketFormSubmitted
}

type ticketFormSubmitted struct {
	Title         string
	Body          string
	PhaseSlug     string
	Wave          int
	DependsOn     []string
	Parallelizable []string
}

func (a *app) handleTicketNewForm(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	phases, err := a.deps.Service.ListPhases(r.Context(), slug)
	if err != nil {
		a.deps.Logger.Warn("ticket new: list phases", "err", err)
		phases = nil
	}
	a.renderer.Page(w, r, "tickets/new", PageOpts{
		Title:       "New ticket · " + proj.Name,
		CurrentSlug: slug,
		Body: ticketFormData{
			Mode:    "new",
			Project: proj,
			Phases:  phases,
		},
	})
}

func (a *app) handleTicketCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	in := parseTicketForm(r)
	createIn := domain.CreateTicketInput{
		ProjectIDOrSlug:    slug,
		Title:              in.Title,
		Body:               in.Body,
		DependsOn:          in.DependsOn,
		ParallelizableWith: in.Parallelizable,
		Wave:               in.Wave,
	}
	if in.PhaseSlug != "" {
		ps := in.PhaseSlug
		createIn.PhaseIDOrSlug = &ps
	}
	tkt, err := a.deps.Service.CreateTicket(r.Context(), createIn)
	if err != nil {
		phases, _ := a.deps.Service.ListPhases(r.Context(), slug)
		w.WriteHeader(classifyServiceError(err))
		a.renderer.Page(w, r, "tickets/new", PageOpts{
			Title:       "New ticket · " + proj.Name,
			CurrentSlug: slug,
			Body: ticketFormData{
				Mode:      "new",
				Project:   proj,
				Phases:    phases,
				FormError: err.Error(),
				Submitted: in,
			},
		})
		return
	}
	SetFlash(w, r, "success", "Ticket created.")
	http.Redirect(w, r, "/tickets/"+tkt.ID+"?slug="+slug, http.StatusSeeOther)
}

// parseTicketForm extracts the shared submitted-form fields. depends_on /
// parallelizable_with accept either repeated form fields (multi-select
// checkboxes) OR a comma-separated text input (the v1 form uses the latter
// to avoid shipping a type-ahead picker before there's UX value in it).
func parseTicketForm(r *http.Request) ticketFormSubmitted {
	wave, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("wave")))
	return ticketFormSubmitted{
		Title:          strings.TrimSpace(r.Form.Get("title")),
		Body:           r.Form.Get("body"),
		PhaseSlug:      strings.TrimSpace(r.Form.Get("phase")),
		Wave:           wave,
		DependsOn:      collectIDs(r.Form["depends_on"], r.Form.Get("depends_on_inline")),
		Parallelizable: collectIDs(r.Form["parallelizable_with"], r.Form.Get("parallelizable_with_inline")),
	}
}

// collectIDs merges repeated form values + a comma-separated string into one
// slice, trimming whitespace and dropping empties. Either input shape works
// so the form can move to checkboxes/multi-select later without changing the
// handler.
func collectIDs(multi []string, inline string) []string {
	out := make([]string, 0, len(multi)+4)
	for _, s := range multi {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	for _, s := range strings.Split(inline, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// --- detail ---------------------------------------------------------------

type ticketDetailData struct {
	Project     *domain.Project
	Phases      []*domain.Phase
	Phase       *domain.Phase // nil for phase-less tickets
	Ticket      *domain.Ticket
	Depends     []*domain.Ticket
	Blocks      []*domain.Ticket
	ProjectSlug string // forwarded to action URLs as the ?slug= hint
	CSRF        string
	IsDone      bool
	Comments    commentsThreadData
}

func (a *app) handleTicketDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tkt, err := a.deps.Service.GetTicket(r.Context(), id)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	// Resolve project from the ticket's ProjectID. ListProjects then match
	// — cheap because the resident registry is in-memory.
	projects, err := a.deps.Service.ListProjects(r.Context())
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	var proj *domain.Project
	for _, p := range projects {
		if p.ID == tkt.ProjectID {
			proj = p
			break
		}
	}
	if proj == nil {
		a.renderer.Error(w, r, http.StatusNotFound, errors.New("ticket's project is not mounted"))
		return
	}
	phases, err := a.deps.Service.ListPhases(r.Context(), proj.Slug)
	if err != nil {
		a.deps.Logger.Warn("ticket detail: list phases", "err", err)
		phases = nil
	}
	var phase *domain.Phase
	if tkt.PhaseID != nil {
		for _, p := range phases {
			if p.ID == *tkt.PhaseID {
				phase = p
				break
			}
		}
	}
	// Hydrate depends/blocks lists. Cheap: ListTickets once, project-scoped.
	depends, blocks := a.dependencyLinks(r, proj.Slug, tkt)

	// Comments thread data — best-effort. ListComments failure logs and
	// renders an empty thread; the detail page is still useful without it.
	csrf := a.summaryCSRF(r)
	threadComments, err := a.deps.Service.ListComments(r.Context(), tkt.ID)
	if err != nil {
		a.deps.Logger.Warn("ticket detail: list comments", "err", err)
		threadComments = nil
	}
	thread := commentsThreadData{
		TicketID:    tkt.ID,
		ProjectSlug: proj.Slug,
		CSRF:        csrf,
		Rows:        commentRows(threadComments),
	}

	a.renderer.Page(w, r, "tickets/detail", PageOpts{
		Title:       tkt.Title + " · " + proj.Name,
		CurrentSlug: proj.Slug,
		Body: ticketDetailData{
			Project:     proj,
			Phases:      phases,
			Phase:       phase,
			Ticket:      tkt,
			Depends:     depends,
			Blocks:      blocks,
			ProjectSlug: proj.Slug,
			CSRF:        csrf,
			IsDone:      tkt.Column == domain.ColumnDone,
			Comments:    thread,
		},
	})
}

// dependencyLinks returns the ticket pointers for the rows in tkt.DependsOn
// and the rows whose DependsOn includes tkt.ID. Best-effort: any ListTickets
// failure degrades to empty slices since the detail page is still useful
// without the dep panels.
func (a *app) dependencyLinks(r *http.Request, slug string, tkt *domain.Ticket) ([]*domain.Ticket, []*domain.Ticket) {
	all, _, err := a.deps.Service.ListTickets(r.Context(), domain.ListTicketsInput{
		ProjectIDOrSlug: slug,
		Limit:           200,
	})
	if err != nil {
		return nil, nil
	}
	byID := map[string]*domain.Ticket{}
	for _, t := range all {
		byID[t.ID] = t
	}
	depends := make([]*domain.Ticket, 0, len(tkt.DependsOn))
	for _, id := range tkt.DependsOn {
		if t, ok := byID[id]; ok {
			depends = append(depends, t)
		}
	}
	blocks := make([]*domain.Ticket, 0)
	for _, t := range all {
		for _, d := range t.DependsOn {
			if d == tkt.ID {
				blocks = append(blocks, t)
				break
			}
		}
	}
	return depends, blocks
}

// --- edit form / update ---------------------------------------------------

func (a *app) handleTicketEditForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tkt, err := a.deps.Service.GetTicket(r.Context(), id)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	if tkt.Column == domain.ColumnDone {
		a.renderer.Error(w, r, http.StatusUnprocessableEntity, errors.New("done tickets are frozen — create a new ticket instead"))
		return
	}
	projects, _ := a.deps.Service.ListProjects(r.Context())
	var proj *domain.Project
	for _, p := range projects {
		if p.ID == tkt.ProjectID {
			proj = p
			break
		}
	}
	if proj == nil {
		a.renderer.Error(w, r, http.StatusNotFound, errors.New("ticket's project is not mounted"))
		return
	}
	phases, _ := a.deps.Service.ListPhases(r.Context(), proj.Slug)
	a.renderer.Page(w, r, "tickets/edit", PageOpts{
		Title:       "Edit " + tkt.Title + " · " + proj.Name,
		CurrentSlug: proj.Slug,
		Body: ticketFormData{
			Mode:    "edit",
			Project: proj,
			Phases:  phases,
			Ticket:  tkt,
			Submitted: ticketFormSubmitted{
				Title:          tkt.Title,
				Body:           tkt.Body,
				Wave:           tkt.Wave,
				DependsOn:      tkt.DependsOn,
				Parallelizable: tkt.ParallelizableWith,
			},
		},
	})
}

func (a *app) handleTicketUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in := parseTicketForm(r)
	wave := in.Wave
	upd := domain.UpdateTicketInput{
		Title: &in.Title,
		Body:  &in.Body,
		Wave:  &wave,
	}
	tkt, err := a.deps.Service.UpdateTicket(r.Context(), id, upd)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	slug := r.URL.Query().Get("slug")
	target := "/tickets/" + tkt.ID
	if slug != "" {
		target += "?slug=" + slug
	}
	SetFlash(w, r, "success", "Ticket updated.")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// --- move + complete ------------------------------------------------------

func (a *app) handleTicketMove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	target := strings.TrimSpace(r.Form.Get("target_column"))
	comment := r.Form.Get("comment")
	if target == string(domain.ColumnDone) {
		// UI shouldn't put `done` in the move dropdown; if a stale form
		// or a hand-rolled POST tries it, send the user to the completion
		// form instead of leaking the service-level rejection.
		a.renderer.Error(w, r, http.StatusUnprocessableEntity, errors.New("done is reachable only via the Complete form, not Move"))
		return
	}
	if _, err := a.deps.Service.MoveTicket(r.Context(), id, domain.Column(target), comment); err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	slug := r.URL.Query().Get("slug")
	loc := "/tickets/" + id
	if slug != "" {
		loc += "?slug=" + slug
	}
	SetFlash(w, r, "success", "Ticket moved.")
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

func (a *app) handleTicketComplete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	te := r.Form.Get("testing_evidence")
	ws := r.Form.Get("work_summary")
	ln := r.Form.Get("learnings")
	if _, err := a.deps.Service.CompleteTicket(r.Context(), id, te, ws, ln); err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	slug := r.URL.Query().Get("slug")
	loc := "/tickets/" + id
	if slug != "" {
		loc += "?slug=" + slug
	}
	SetFlash(w, r, "success", "Ticket completed.")
	http.Redirect(w, r, loc, http.StatusSeeOther)
}
