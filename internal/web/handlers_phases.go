package web

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"tickets_please/internal/domain"
)

// Phases & waves CRUD. Mirrors handlers_projects.go's shape:
//   - withCSRF wraps every POST (router applies it via wrap()).
//   - classifyServiceError maps domain sentinels to HTTP statuses for
//     inline form responses.
//   - Summary editor uses the same view/edit partial swap pattern.
//
// Path convention: phases live under their project, so URLs are
// /p/{slug}/phases/... — ticket 4 owns these paths exclusively, no overlap
// with tickets 3/5/7.
//
// Cross-cutting endpoint: POST /tickets/{id}/assign-phase reassigns a ticket
// between phases (or to phase-less). The "?slug=" query hint convention is
// reserved here for ticket 5 to share, even though Service.AssignTicketToPhase
// doesn't currently take a slug — the hint would skip
// hostStoreForTicket's O(mounts) walk if Service grows a hinted variant later.

// --- list / create ---------------------------------------------------------

type phasesIndexData struct {
	Project *domain.Project
	Phases  []phaseWithWaves
}

// phaseWithWaves carries a phase + the wave-bucketed tickets that belong to
// it, so the index can render each phase as a collapsible drill-down without
// a second round-trip from the template.
//
// Dist is the count-per-column for tickets in this phase, Total is their
// sum. The summary row uses both to render a mini status-bar without a
// second walk over the tickets in the template.
type phaseWithWaves struct {
	Phase *domain.Phase
	Waves []waveSection
	Dist  phaseDist
	Total int
}

// waveSection is one wave's worth of tickets inside a phase. IsUnassigned
// flags wave 0 (the "soft default" bucket) so templates can mute it.
type waveSection struct {
	Wave         int
	Tickets      []*domain.Ticket
	IsUnassigned bool
}

// phaseDist counts tickets per kanban column for one phase. The template
// reads percentage segments off it via the percentOf helper.
type phaseDist struct {
	Todo, InProgress, Testing, Done int
}

func (a *app) handlePhasesIndex(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	phases, err := a.deps.Service.ListPhases(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	tickets, _, err := a.deps.Service.ListTickets(r.Context(), domain.ListTicketsInput{
		ProjectIDOrSlug: slug,
		Limit:           200,
	})
	if err != nil {
		// Tickets failure degrades to phases-without-waves rather than 500ing.
		// The page still renders useful info; a missing wave breakdown is a
		// soft failure.
		a.deps.Logger.Warn("phases: list tickets for index", "err", err)
		tickets = nil
	}
	enriched := bucketTicketsByPhaseAndWave(phases, tickets)
	a.renderer.Page(w, r, "phases/index", PageOpts{
		Title:       proj.Name + " · phases · tickets_please",
		CurrentSlug: slug,
		Body:        phasesIndexData{Project: proj, Phases: enriched},
	})
}

// bucketTicketsByPhaseAndWave groups tickets by (phase, wave) and returns the
// phases (in their input order — caller decides ordering) each carrying the
// per-wave sections of *their* tickets. Tickets with PhaseID == nil OR with
// a PhaseID that doesn't match any phase are dropped from this output —
// the phases index intentionally excludes phase-less tickets (the waves
// page surfaces them via the "Unphased" column). Orphan PhaseIDs are
// logged at the call site, not here, since this helper has no logger.
func bucketTicketsByPhaseAndWave(phases []*domain.Phase, tickets []*domain.Ticket) []phaseWithWaves {
	// Index tickets by phase ID for O(1) lookup per phase.
	byPhase := map[string][]*domain.Ticket{}
	for _, t := range tickets {
		if t.PhaseID == nil {
			continue
		}
		byPhase[*t.PhaseID] = append(byPhase[*t.PhaseID], t)
	}
	out := make([]phaseWithWaves, 0, len(phases))
	for _, ph := range phases {
		mine := byPhase[ph.ID]
		var dist phaseDist
		for _, t := range mine {
			switch t.Column {
			case domain.ColumnTodo:
				dist.Todo++
			case domain.ColumnInProgress:
				dist.InProgress++
			case domain.ColumnTesting:
				dist.Testing++
			case domain.ColumnDone:
				dist.Done++
			}
		}
		out = append(out, phaseWithWaves{
			Phase: ph,
			Waves: bucketTicketsByWave(mine),
			Dist:  dist,
			Total: len(mine),
		})
	}
	return out
}

// bucketTicketsByWave groups tickets by wave number, ordered ascending with
// wave 0 (unassigned) sorted last — matches svc.ListWaves and handleWaves.
// Within each wave tickets are sorted by title for deterministic rendering.
func bucketTicketsByWave(tickets []*domain.Ticket) []waveSection {
	if len(tickets) == 0 {
		return nil
	}
	buckets := map[int][]*domain.Ticket{}
	for _, t := range tickets {
		buckets[t.Wave] = append(buckets[t.Wave], t)
	}
	for _, ts := range buckets {
		sort.Slice(ts, func(i, j int) bool { return ts[i].Title < ts[j].Title })
	}
	waves := make([]int, 0, len(buckets))
	for w := range buckets {
		waves = append(waves, w)
	}
	sort.Slice(waves, func(i, j int) bool {
		ai, aj := waves[i], waves[j]
		if ai == 0 {
			ai = int(^uint(0) >> 1)
		}
		if aj == 0 {
			aj = int(^uint(0) >> 1)
		}
		return ai < aj
	})
	out := make([]waveSection, 0, len(waves))
	for _, w := range waves {
		out = append(out, waveSection{
			Wave:         w,
			Tickets:      buckets[w],
			IsUnassigned: w == 0,
		})
	}
	return out
}

type phaseFormData struct {
	Mode      string // "new" or "edit"
	Project   *domain.Project
	Phase     *domain.Phase
	FormError string
	Submitted phaseFormSubmitted
}

type phaseFormSubmitted struct {
	Slug        string
	Name        string
	Description string
	Summary     string
}

func (a *app) handlePhaseNewForm(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	a.renderer.Page(w, r, "phases/new", PageOpts{
		Title:       "New phase · " + proj.Name,
		CurrentSlug: proj.Slug,
		Body: phaseFormData{
			Mode:    "new",
			Project: proj,
		},
	})
}

func (a *app) handlePhaseCreate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	in := phaseFormSubmitted{
		Name:        strings.TrimSpace(r.Form.Get("name")),
		Description: r.Form.Get("description"),
		Summary:     r.Form.Get("summary"),
	}
	phase, err := a.deps.Service.CreatePhase(r.Context(), slug, in.Name, in.Description, in.Summary)
	if err != nil {
		a.renderPhaseFormError(w, r, "phases/new", "new", proj, nil, in, err)
		return
	}
	SetFlash(w, r, "success", "Phase "+phase.Name+" created.")
	http.Redirect(w, r, "/p/"+proj.Slug+"/phases/"+phase.Slug, http.StatusSeeOther)
}

func (a *app) renderPhaseFormError(w http.ResponseWriter, r *http.Request, page, mode string, proj *domain.Project, phase *domain.Phase, in phaseFormSubmitted, err error) {
	w.WriteHeader(classifyServiceError(err))
	title := "New phase · " + proj.Name
	if mode == "edit" && phase != nil {
		title = "Edit " + phase.Name + " · " + proj.Name
	}
	a.renderer.Page(w, r, page, PageOpts{
		Title:       title,
		CurrentSlug: proj.Slug,
		Body: phaseFormData{
			Mode:      mode,
			Project:   proj,
			Phase:     phase,
			FormError: err.Error(),
			Submitted: in,
		},
	})
}

// --- detail / edit / update / delete --------------------------------------

type phaseDetailData struct {
	Project *domain.Project
	Phase   *domain.Phase
	Waves   []waveSection
}

func (a *app) handlePhaseDetail(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	phaseSlug := r.PathValue("phase")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	phase, err := a.deps.Service.GetPhase(r.Context(), slug, phaseSlug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	// Pull tickets scoped to this phase, then bucket by wave the same way the
	// phases-index expanded view does so both pages render identically.
	tickets, _, err := a.deps.Service.ListTickets(r.Context(), domain.ListTicketsInput{
		ProjectIDOrSlug: slug,
		PhaseIDOrSlug:   &phaseSlug,
		Limit:           200,
	})
	if err != nil {
		// Soft-degrade: an empty wave list still renders the rest of the page.
		a.deps.Logger.Warn("phases: list tickets for detail", "err", err)
		tickets = nil
	}
	waves := bucketTicketsByWave(tickets)
	a.renderer.Page(w, r, "phases/detail", PageOpts{
		Title:       phase.Name + " · " + proj.Name,
		CurrentSlug: proj.Slug,
		Body: phaseDetailData{
			Project: proj,
			Phase:   phase,
			Waves:   waves,
		},
	})
}

func (a *app) handlePhaseEditForm(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	phaseSlug := r.PathValue("phase")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	phase, err := a.deps.Service.GetPhase(r.Context(), slug, phaseSlug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	a.renderer.Page(w, r, "phases/edit", PageOpts{
		Title:       "Edit " + phase.Name + " · " + proj.Name,
		CurrentSlug: proj.Slug,
		Body: phaseFormData{
			Mode:    "edit",
			Project: proj,
			Phase:   phase,
			Submitted: phaseFormSubmitted{
				Slug:        phase.Slug,
				Name:        phase.Name,
				Description: phase.Description,
			},
		},
	})
}

func (a *app) handlePhaseUpdate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	phaseSlug := r.PathValue("phase")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	phase, err := a.deps.Service.GetPhase(r.Context(), slug, phaseSlug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	in := phaseFormSubmitted{
		Slug:        phase.Slug,
		Name:        strings.TrimSpace(r.Form.Get("name")),
		Description: r.Form.Get("description"),
	}
	updateIn := domain.UpdatePhaseInput{Name: &in.Name, Description: &in.Description}
	if _, err := a.deps.Service.UpdatePhase(r.Context(), slug, phaseSlug, updateIn); err != nil {
		a.renderPhaseFormError(w, r, "phases/edit", "edit", proj, phase, in, err)
		return
	}
	SetFlash(w, r, "success", "Phase updated.")
	http.Redirect(w, r, "/p/"+slug+"/phases/"+phase.Slug, http.StatusSeeOther)
}

func (a *app) handlePhaseDelete(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	phaseSlug := r.PathValue("phase")
	if r.Form.Get("confirm") != "yes" {
		a.renderer.Error(w, r, http.StatusBadRequest, errors.New("delete requires explicit confirmation; use the form on the phase page"))
		return
	}
	if err := a.deps.Service.DeletePhase(r.Context(), slug, phaseSlug); err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	SetFlash(w, r, "success", "Phase "+phaseSlug+" deleted.")
	http.Redirect(w, r, "/p/"+slug+"/phases", http.StatusSeeOther)
}

// --- summary view + in-place editor ---------------------------------------

type phaseSummaryData struct {
	Project   *domain.Project
	Phase     *domain.Phase
	Mode      string // "view" or "edit"
	Summary   string
	FormError string
	CSRF      string
}

func (a *app) handlePhaseSummaryView(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	phaseSlug := r.PathValue("phase")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	phase, err := a.deps.Service.GetPhase(r.Context(), slug, phaseSlug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	mode := "view"
	if r.URL.Query().Get("edit") == "1" {
		mode = "edit"
	}
	body := phaseSummaryData{
		Project: proj,
		Phase:   phase,
		Mode:    mode,
		Summary: phase.Summary,
		CSRF:    a.summaryCSRF(r),
	}
	if r.Header.Get("HX-Request") == "true" {
		partial := "phase_summary_view"
		if mode == "edit" {
			partial = "phase_summary_edit"
		}
		a.renderer.Partial(w, r, partial, body)
		return
	}
	a.renderer.Page(w, r, "phases/summary", PageOpts{
		Title:       phase.Name + " · summary · " + proj.Name,
		CurrentSlug: proj.Slug,
		Body:        body,
	})
}

func (a *app) handlePhaseSummaryUpdate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	phaseSlug := r.PathValue("phase")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	phase, err := a.deps.Service.GetPhase(r.Context(), slug, phaseSlug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	summary := r.Form.Get("summary")
	csrf := a.summaryCSRF(r)
	if _, err := a.deps.Service.UpdatePhase(r.Context(), slug, phaseSlug, domain.UpdatePhaseInput{Summary: &summary}); err != nil {
		w.WriteHeader(classifyServiceError(err))
		body := phaseSummaryData{
			Project: proj, Phase: phase, Mode: "edit",
			Summary: summary, FormError: err.Error(), CSRF: csrf,
		}
		if r.Header.Get("HX-Request") == "true" {
			a.renderer.Partial(w, r, "phase_summary_edit", body)
			return
		}
		a.renderer.Page(w, r, "phases/summary", PageOpts{
			Title: phase.Name + " · summary · " + proj.Name, CurrentSlug: proj.Slug, Body: body,
		})
		return
	}
	updated, err := a.deps.Service.GetPhase(r.Context(), slug, phaseSlug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	body := phaseSummaryData{Project: proj, Phase: updated, Mode: "view", Summary: updated.Summary, CSRF: csrf}
	if r.Header.Get("HX-Request") == "true" {
		a.renderer.Partial(w, r, "phase_summary_view", body)
		return
	}
	SetFlash(w, r, "success", "Summary updated.")
	http.Redirect(w, r, "/p/"+slug+"/phases/"+phase.Slug+"/summary", http.StatusSeeOther)
}

// --- assign ticket to phase -----------------------------------------------

// handleAssignTicketToPhase serves POST /tickets/{id}/assign-phase. Form
// fields: `phase` (phase id/slug; empty = no phase) and `comment` (required).
// On success, redirects to the ticket detail page (/tickets/{id}, ticket 5
// owns) so the user lands on the canonical view of the just-moved ticket.
//
// On AssignTicketToPhase's "comment required" error (or any other svc
// error), renders an inline error partial — htmx swap target where the form
// lives expects an error.tmpl-shape response.
func (a *app) handleAssignTicketToPhase(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	comment := r.Form.Get("comment")
	phaseSlug := r.Form.Get("phase")

	var phasePtr *string
	if phaseSlug != "" {
		phasePtr = &phaseSlug
	}

	if _, err := a.deps.Service.AssignTicketToPhase(r.Context(), id, phasePtr, comment); err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}

	// Slug hint forwarded back through the redirect so the ticket detail
	// handler in ticket 5 can use it to skip hostStoreForTicket. The /
	// catch-all 404s today; this URL goes live once ticket 5 lands.
	target := "/tickets/" + id
	if hint := r.URL.Query().Get("slug"); hint != "" {
		target += "?slug=" + hint
	}
	SetFlash(w, r, "success", "Ticket reassigned.")
	http.Redirect(w, r, target, http.StatusSeeOther)
}
