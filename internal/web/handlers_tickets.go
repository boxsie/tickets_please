package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
	pgtickets "tickets_please/internal/web/components/pages/tickets"
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

// --- board (retired) -------------------------------------------------------

// handleBoardRedirect is all that remains of the Trello board page. The board
// was useless at scale ("nothing fits in and its not readable" — the user), so
// phases→waves is now the spine. We keep the old URL alive as a permanent 302
// to the phases page so stale bookmarks, agent memory, and old comment links
// don't 404. Cheap insurance — never remove it.
func (a *app) handleBoardRedirect(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	http.Redirect(w, r, "/p/"+slug+"/phases", http.StatusFound)
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
	Title          string
	Body           string
	PhaseSlug      string
	Wave           int
	DependsOn      []string
	Parallelizable []string
}

func (a *app) handleTicketNewForm(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	phases, err := a.deps.Service.ListPhases(r.Context(), slug)
	if err != nil {
		a.deps.Logger.Warn("ticket new: list phases", "err", err)
		phases = nil
	}
	a.renderer.RenderTempl(w, r, PageOpts{
		Title:       "New ticket · " + proj.Name,
		CurrentSlug: slug,
	}, pgtickets.New(pgtickets.FormProps{
		Mode:    "new",
		Project: proj,
		Phases:  phases,
		CSRF:    a.summaryCSRF(r),
	}))
}

func (a *app) handleTicketCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
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
		a.renderer.RenderTempl(w, r, PageOpts{
			Title:       "New ticket · " + proj.Name,
			CurrentSlug: slug,
		}, pgtickets.New(pgtickets.FormProps{
			Mode:      "new",
			Project:   proj,
			Phases:    phases,
			FormError: err.Error(),
			Submitted: pgtickets.FormSubmitted{
				Title:          in.Title,
				Body:           in.Body,
				PhaseSlug:      in.PhaseSlug,
				Wave:           in.Wave,
				DependsOn:      in.DependsOn,
				Parallelizable: in.Parallelizable,
			},
			CSRF: a.summaryCSRF(r),
		}))
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
	Parallel    []*domain.Ticket
	ProjectSlug string // forwarded to action URLs as the ?slug= hint
	CSRF        string
	IsDone      bool
	Comments    commentsThreadData
}

func (a *app) handleTicketDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tkt, err := a.deps.Service.GetTicket(r.Context(), id)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	// Resolve project from the ticket's ProjectID. ListProjects then match
	// — cheap because the resident registry is in-memory.
	projects, err := a.deps.Service.ListProjects(r.Context())
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
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
		a.renderer.RenderTemplError(w, r, http.StatusNotFound, errors.New("ticket's project is not mounted"))
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
	// Hydrate depends/blocks/parallel lists. Cheap: ListTickets once, project-scoped.
	depends, blocks, parallel := a.dependencyLinks(r, proj.Slug, tkt)

	// Comments thread data — best-effort. ListComments failure logs and
	// renders an empty thread; the detail page is still useful without it.
	csrf := a.summaryCSRF(r)
	threadComments, err := a.deps.Service.ListComments(r.Context(), tkt.ID)
	if err != nil {
		a.deps.Logger.Warn("ticket detail: list comments", "err", err)
		threadComments = nil
	}
	thread := pgtickets.CommentsThreadProps{
		TicketID:    tkt.ID,
		ProjectSlug: proj.Slug,
		CSRF:        csrf,
		Rows:        commentRowsTempl(threadComments),
	}

	a.renderer.RenderTempl(w, r, PageOpts{
		Title:       tkt.Title + " · " + proj.Name,
		CurrentSlug: proj.Slug,
	}, pgtickets.Detail(pgtickets.DetailProps{
		Project:     proj,
		Phases:      phases,
		Phase:       phase,
		Ticket:      tkt,
		Depends:     depends,
		Blocks:      blocks,
		Parallel:    parallel,
		ProjectSlug: proj.Slug,
		CSRF:        csrf,
		IsDone:      tkt.Column == domain.ColumnDone,
		Comments:    thread,
	}))
}

// dependencyLinks returns ticket pointers for (1) the rows in tkt.DependsOn,
// (2) the rows whose DependsOn includes tkt.ID (blocks), and (3) the rows in
// tkt.ParallelizableWith. Best-effort: any ListTickets failure degrades to
// empty slices since the detail page is still useful without the dep panels.
func (a *app) dependencyLinks(r *http.Request, slug string, tkt *domain.Ticket) (depends, blocks, parallel []*domain.Ticket) {
	all, _, err := a.deps.Service.ListTickets(r.Context(), domain.ListTicketsInput{
		ProjectIDOrSlug: slug,
		Limit:           200,
	})
	if err != nil {
		return nil, nil, nil
	}
	byID := map[string]*domain.Ticket{}
	for _, t := range all {
		byID[t.ID] = t
	}
	depends = make([]*domain.Ticket, 0, len(tkt.DependsOn))
	for _, id := range tkt.DependsOn {
		if t, ok := byID[id]; ok {
			depends = append(depends, t)
		}
	}
	parallel = make([]*domain.Ticket, 0, len(tkt.ParallelizableWith))
	for _, id := range tkt.ParallelizableWith {
		if t, ok := byID[id]; ok {
			parallel = append(parallel, t)
		}
	}
	blocks = make([]*domain.Ticket, 0)
	for _, t := range all {
		for _, d := range t.DependsOn {
			if d == tkt.ID {
				blocks = append(blocks, t)
				break
			}
		}
	}
	return depends, blocks, parallel
}

// --- edit form / update ---------------------------------------------------

func (a *app) handleTicketEditForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tkt, err := a.deps.Service.GetTicket(r.Context(), id)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	if tkt.Column == domain.ColumnDone {
		a.renderer.RenderTemplError(w, r, http.StatusUnprocessableEntity, errors.New("done tickets are frozen — create a new ticket instead"))
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
		a.renderer.RenderTemplError(w, r, http.StatusNotFound, errors.New("ticket's project is not mounted"))
		return
	}
	phases, _ := a.deps.Service.ListPhases(r.Context(), proj.Slug)
	a.renderer.RenderTempl(w, r, PageOpts{
		Title:       "Edit " + tkt.Title + " · " + proj.Name,
		CurrentSlug: proj.Slug,
	}, pgtickets.Edit(pgtickets.FormProps{
		Mode:    "edit",
		Project: proj,
		Phases:  phases,
		Ticket:  tkt,
		Submitted: pgtickets.FormSubmitted{
			Title:          tkt.Title,
			Body:           tkt.Body,
			Wave:           tkt.Wave,
			DependsOn:      tkt.DependsOn,
			Parallelizable: tkt.ParallelizableWith,
		},
		CSRF: a.summaryCSRF(r),
	}))
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
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
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
	ctx := r.Context()
	clientID := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	optimistic := clientID != ""
	if optimistic {
		ctx = svc.WithClientID(ctx, clientID)
	}

	if target == string(domain.ColumnDone) {
		// UI shouldn't put `done` in the move dropdown; if a stale form
		// or a hand-rolled POST tries it, send the user to the completion
		// form instead of leaking the service-level rejection.
		if optimistic {
			http.Error(w, "done is reachable only via the Complete form, not Move", http.StatusUnprocessableEntity)
			return
		}
		a.renderer.RenderTemplError(w, r, http.StatusUnprocessableEntity, errors.New("done is reachable only via the Complete form, not Move"))
		return
	}
	if _, err := a.deps.Service.MoveTicket(ctx, id, domain.Column(target), comment); err != nil {
		if optimistic {
			http.Error(w, err.Error(), classifyServiceError(err))
			return
		}
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	if optimistic {
		// The SSE TicketMoved patch updates the badge/actions live; nothing to
		// render here. The optimistic JS flips its "Moving…" toast on the echo.
		w.WriteHeader(http.StatusNoContent)
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
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
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

// handleTicketArchive / handleTicketUnarchive wrap the existing svc archive
// methods. Both require a non-empty comment (the modal enforces it client-side
// with `required`; the service enforces it server-side and a 422 surfaces
// inline on rejection). Archive is column-independent — done tickets archive
// fine, the freeze rule only covers completion fields. The live archived-badge
// + button flip happens via the SSE TicketArchived/Unarchived patch; the
// redirect below reloads the originating tab to the same detail page.
func (a *app) handleTicketArchive(w http.ResponseWriter, r *http.Request) {
	a.flipArchive(w, r, true)
}

func (a *app) handleTicketUnarchive(w http.ResponseWriter, r *http.Request) {
	a.flipArchive(w, r, false)
}

func (a *app) flipArchive(w http.ResponseWriter, r *http.Request, archive bool) {
	id := r.PathValue("id")
	comment := r.Form.Get("comment")
	var err error
	if archive {
		_, err = a.deps.Service.ArchiveTicket(r.Context(), id, comment)
	} else {
		_, err = a.deps.Service.UnarchiveTicket(r.Context(), id, comment)
	}
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	slug := r.URL.Query().Get("slug")
	loc := "/tickets/" + id
	if slug != "" {
		loc += "?slug=" + slug
	}
	if archive {
		SetFlash(w, r, "success", "Ticket archived.")
	} else {
		SetFlash(w, r, "success", "Ticket unarchived.")
	}
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

// handleTicketDelete hard-deletes a non-`done` ticket via svc.DeleteTicket.
// On success redirects to the project phases page (the ticket detail page is
// gone) with a flash; on a service-level refusal (done ticket, dependents) the
// classifyServiceError mapper turns it into a 422 with the message visible.
func (a *app) handleTicketDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if err := a.deps.Service.DeleteTicket(r.Context(), id); err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	loc := "/"
	if slug != "" {
		loc = "/p/" + slug + "/phases"
	}
	SetFlash(w, r, "success", "Ticket deleted.")
	http.Redirect(w, r, loc, http.StatusSeeOther)
}
