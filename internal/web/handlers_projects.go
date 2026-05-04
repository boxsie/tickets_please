package web

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
)

// Projects CRUD handlers — wraps the eight project-related Service methods
// in HTML/htmx routes. Routes registered in router.go using Go 1.22+
// method+pattern matching.
//
// Hard rules surfaced inline:
//   - Summary >= 200 chars on create / update — server enforces, UI shows
//     a 422 partial via renderer.Error when ErrInvalidArgument bubbles up.
//   - Slug unique + URL-safe — same surface path.
//   - Slug immutable post-create — edit form has no slug field.
//   - Delete is destructive — POST-only, requires CSRF + an X-Confirm: yes
//     header (set by the form's hidden input) so a stray browser nav doesn't
//     trigger it.
//
// Sidebar refresh: POST /p, POST /p/load, POST /p/{slug}/delete all set
// `HX-Trigger: sidebar-refresh` BEFORE WriteHeader so the sidebar (per ticket
// 2's contract) re-fetches /p and re-renders.

// --- list / create ----------------------------------------------------------

// projectsIndexData is the payload for pages/projects/index.tmpl.
type projectsIndexData struct {
	Projects []*domain.Project
}

// handleProjectsIndex serves GET /p — the full project list. Same data as the
// sidebar shows, but in a denser table view that future tickets can extend
// with per-project metrics.
func (a *app) handleProjectsIndex(w http.ResponseWriter, r *http.Request) {
	projects, err := a.deps.Service.ListProjects(r.Context())
	if err != nil {
		a.deps.Logger.Error("projects: list", "err", err)
		a.renderer.Error(w, r, http.StatusInternalServerError, err)
		return
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Slug < projects[j].Slug })
	a.renderer.Page(w, r, "projects/index", PageOpts{
		Title: "Projects · tickets_please",
		Body:  projectsIndexData{Projects: projects},
	})
}

// projectFormData is the shared payload for new/edit forms. The form template
// is generic over create-vs-update so the same fields render in both pages.
type projectFormData struct {
	// Mode is "new" or "edit"; controls the form's action URL and which
	// fields render.
	Mode string
	// Project is the existing record on edit, nil on new.
	Project *domain.Project
	// FormError is the inline validation message. Empty when no error.
	FormError string
	// Submitted captures user input on validation failure so the form
	// re-renders with their typed values rather than blank fields.
	Submitted projectFormSubmitted
}

type projectFormSubmitted struct {
	Slug        string
	Name        string
	Description string
	Summary     string
}

// handleProjectNewForm serves GET /p/new — the create form.
func (a *app) handleProjectNewForm(w http.ResponseWriter, r *http.Request) {
	a.renderer.Page(w, r, "projects/new", PageOpts{
		Title: "New project · tickets_please",
		Body: projectFormData{
			Mode: "new",
		},
	})
}

// handleProjectCreate handles POST /p. Validates inputs, calls
// CreateProject, sets sidebar-refresh trigger on success, redirects to the
// new project's detail page with a flash. On validation errors, re-renders
// the form inline with the error message and the user's typed values.
func (a *app) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	in := projectFormSubmitted{
		Slug:        strings.TrimSpace(r.Form.Get("slug")),
		Name:        strings.TrimSpace(r.Form.Get("name")),
		Description: r.Form.Get("description"),
		Summary:     r.Form.Get("summary"),
	}

	proj, err := a.deps.Service.CreateProject(r.Context(), in.Slug, in.Name, in.Description, in.Summary)
	if err != nil {
		a.renderProjectFormError(w, r, "projects/new", "new", nil, in, err)
		return
	}

	w.Header().Set("HX-Trigger", "sidebar-refresh")
	SetFlash(w, r, "success", "Project "+proj.Slug+" created.")
	http.Redirect(w, r, "/p/"+proj.Slug, http.StatusSeeOther)
}

// renderProjectFormError re-renders a project form (new or edit) with an
// inline error message. Status code matches the kind of error so XHR clients
// can branch on it (422 for validation, 409 for conflicts, 500 otherwise).
func (a *app) renderProjectFormError(w http.ResponseWriter, r *http.Request, page, mode string, existing *domain.Project, in projectFormSubmitted, err error) {
	status := classifyServiceError(err)
	w.WriteHeader(status)
	a.renderer.Page(w, r, page, PageOpts{
		Title: titleForFormError(mode),
		Body: projectFormData{
			Mode:      mode,
			Project:   existing,
			FormError: err.Error(),
			Submitted: in,
		},
	})
}

func titleForFormError(mode string) string {
	if mode == "edit" {
		return "Edit project · tickets_please"
	}
	return "New project · tickets_please"
}

// classifyServiceError maps domain sentinel errors to HTTP statuses for
// inline form responses. The body of the error (the post-colon message) is
// what the user sees; the status drives the right curl/htmx branch.
func classifyServiceError(err error) int {
	switch {
	case errors.Is(err, domain.ErrInvalidArgument), errors.Is(err, domain.ErrFailedPrecondition):
		return http.StatusUnprocessableEntity
	case errors.Is(err, domain.ErrAlreadyExists):
		return http.StatusConflict
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, domain.ErrUnauthenticated):
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}

// --- mount existing -------------------------------------------------------

// loadProjectFormData is the payload for pages/projects/load.tmpl.
type loadProjectFormData struct {
	FormError string
	Path      string
	Picker    fsListing
}

// handleLoadProjectForm serves GET /p/load — the mount-from-disk form
// rooted at $HOME (or `?path=` if supplied) plus a manual-entry fallback.
func (a *app) handleLoadProjectForm(w http.ResponseWriter, r *http.Request) {
	startPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if startPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			startPath = home
		} else {
			startPath = "/"
		}
	}
	a.renderer.Page(w, r, "projects/load", PageOpts{
		Title: "Load project · tickets_please",
		Body:  loadProjectFormData{Picker: buildFSListing(startPath)},
	})
}

// handleLoadProjectMount handles POST /p/load — calls RegisterProjectMount
// with the absolute path the user supplied. On success, sets sidebar-refresh
// + flash and redirects to the newly-mounted project's detail page.
func (a *app) handleLoadProjectMount(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.Form.Get("path"))
	if path == "" {
		a.renderLoadFormError(w, r, "Path is required.", "", http.StatusUnprocessableEntity)
		return
	}
	slug, err := a.deps.Service.RegisterProjectMount(r.Context(), path)
	if err != nil {
		a.renderLoadFormError(w, r, err.Error(), path, classifyServiceError(err))
		return
	}
	w.Header().Set("HX-Trigger", "sidebar-refresh")
	SetFlash(w, r, "success", "Mounted "+slug+" from "+path+".")
	http.Redirect(w, r, "/p/"+slug, http.StatusSeeOther)
}

// renderLoadFormError re-renders /p/load with an inline error and the
// picker rooted at the offending path's parent (so the user can quickly
// correct the click).
func (a *app) renderLoadFormError(w http.ResponseWriter, r *http.Request, msg, path string, status int) {
	startPath := path
	if startPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			startPath = home
		} else {
			startPath = "/"
		}
	}
	w.WriteHeader(status)
	a.renderer.Page(w, r, "projects/load", PageOpts{
		Title: "Load project · tickets_please",
		Body: loadProjectFormData{
			FormError: msg,
			Path:      path,
			Picker:    buildFSListing(startPath),
		},
	})
}

// --- detail / edit / update / delete --------------------------------------

// projectDetailData is the payload for pages/projects/detail.tmpl. Phases
// list is included so the detail page can render a tab strip with counts
// (the Phases tab links to ticket 4's UI).
type projectDetailData struct {
	Project    *domain.Project
	Phases     []*domain.Phase
	PhaseCount int
}

// handleProjectDetail serves GET /p/{slug}. Header + tabs (Board, Phases,
// Summary) + phase-count breadcrumbs. Tickets/Phases tabs route to ticket 4
// and ticket 5 respectively; both 404 today, which is fine for the
// foundation since the project page itself renders.
func (a *app) handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	phases, _ := a.deps.Service.ListPhases(r.Context(), proj.Slug)

	a.renderer.Page(w, r, "projects/detail", PageOpts{
		Title:       proj.Name + " · tickets_please",
		CurrentSlug: proj.Slug,
		Body: projectDetailData{
			Project:    proj,
			Phases:     phases,
			PhaseCount: len(phases),
		},
	})
}

// handleProjectEditForm serves GET /p/{slug}/edit. Renders the form
// pre-filled from the current project record. Slug is shown read-only.
func (a *app) handleProjectEditForm(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	a.renderer.Page(w, r, "projects/edit", PageOpts{
		Title:       "Edit " + proj.Name + " · tickets_please",
		CurrentSlug: proj.Slug,
		Body: projectFormData{
			Mode:    "edit",
			Project: proj,
			Submitted: projectFormSubmitted{
				Slug:        proj.Slug,
				Name:        proj.Name,
				Description: proj.Description,
				// Summary deliberately not populated here — the dedicated
				// summary editor handles that to keep the edit form small.
			},
		},
	})
}

// handleProjectUpdate handles POST /p/{slug} — name + description only.
// Slug is immutable; summary lives behind the dedicated summary editor.
func (a *app) handleProjectUpdate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	in := projectFormSubmitted{
		Slug:        proj.Slug,
		Name:        strings.TrimSpace(r.Form.Get("name")),
		Description: r.Form.Get("description"),
	}
	updateIn := domain.UpdateProjectInput{
		Name:        &in.Name,
		Description: &in.Description,
	}
	if _, err := a.deps.Service.UpdateProject(r.Context(), slug, updateIn); err != nil {
		a.renderProjectFormError(w, r, "projects/edit", "edit", proj, in, err)
		return
	}
	SetFlash(w, r, "success", "Project updated.")
	http.Redirect(w, r, "/p/"+proj.Slug, http.StatusSeeOther)
}

// handleProjectDelete handles POST /p/{slug}/delete. Requires the form to
// carry `confirm=yes` so a misconfigured form (no hidden confirm field) or a
// CSRF-bypass via a stale tab can't blow away a project. Service refuses if
// the project still has active tickets — surfaced inline.
func (a *app) handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if r.Form.Get("confirm") != "yes" {
		a.renderer.Error(w, r, http.StatusBadRequest, errors.New("delete requires explicit confirmation; use the form on the project page"))
		return
	}
	if err := a.deps.Service.DeleteProject(r.Context(), slug); err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	w.Header().Set("HX-Trigger", "sidebar-refresh")
	SetFlash(w, r, "success", "Project "+slug+" deleted.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- summary view + in-place editor ---------------------------------------

// projectSummaryData is the payload for pages/projects/summary.tmpl and the
// project_summary_view/edit partials. Mode controls which sub-template the
// page renders. CSRF is embedded so the edit partial can render the form
// hidden field even when invoked as a Partial (where .Chrome isn't reachable).
type projectSummaryData struct {
	Project   *domain.Project
	Mode      string // "view" or "edit"
	Summary   string
	FormError string
	CSRF      string
}

// summaryCSRF returns the CSRF token for the current request, or "" when no
// session is bound. Hoisted so both the view and update handlers populate the
// partial payload identically.
func (a *app) summaryCSRF(r *http.Request) string {
	id, ok := svc.SessionIDFrom(r.Context())
	if !ok {
		return ""
	}
	return csrfToken(a.session.secret, id)
}

// handleProjectSummaryView serves GET /p/{slug}/summary. ?edit=1 swaps to
// the editor textarea; the htmx in-place editor uses that to flip between
// view and edit without a full page reload.
func (a *app) handleProjectSummaryView(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	mode := "view"
	if r.URL.Query().Get("edit") == "1" {
		mode = "edit"
	}
	body := projectSummaryData{
		Project: proj,
		Mode:    mode,
		Summary: proj.Summary,
		CSRF:    a.summaryCSRF(r),
	}

	// htmx swap target for the in-place editor: just the view/edit fragment.
	if r.Header.Get("HX-Request") == "true" {
		partial := "project_summary_view"
		if mode == "edit" {
			partial = "project_summary_edit"
		}
		a.renderer.Partial(w, r, partial, body)
		return
	}

	a.renderer.Page(w, r, "projects/summary", PageOpts{
		Title:       proj.Name + " · summary · tickets_please",
		CurrentSlug: proj.Slug,
		Body:        body,
	})
}

// handleProjectSummaryUpdate handles POST /p/{slug}/summary. On success
// returns the rendered view partial (for htmx in-place editor) or redirects
// to the summary page (for non-htmx fallback). On validation failure renders
// the edit partial / page with the inline error.
func (a *app) handleProjectSummaryUpdate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	summary := r.Form.Get("summary")

	csrf := a.summaryCSRF(r)
	if _, err := a.deps.Service.UpdateProject(r.Context(), slug, domain.UpdateProjectInput{Summary: &summary}); err != nil {
		status := classifyServiceError(err)
		w.WriteHeader(status)
		body := projectSummaryData{
			Project:   proj,
			Mode:      "edit",
			Summary:   summary,
			FormError: err.Error(),
			CSRF:      csrf,
		}
		if r.Header.Get("HX-Request") == "true" {
			a.renderer.Partial(w, r, "project_summary_edit", body)
			return
		}
		a.renderer.Page(w, r, "projects/summary", PageOpts{
			Title:       proj.Name + " · summary · tickets_please",
			CurrentSlug: proj.Slug,
			Body:        body,
		})
		return
	}

	// Re-fetch so the rendered view sees the new summary.
	updated, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}

	body := projectSummaryData{Project: updated, Mode: "view", Summary: updated.Summary, CSRF: csrf}
	if r.Header.Get("HX-Request") == "true" {
		a.renderer.Partial(w, r, "project_summary_view", body)
		return
	}
	SetFlash(w, r, "success", "Summary updated.")
	http.Redirect(w, r, "/p/"+slug+"/summary", http.StatusSeeOther)
}

// --- sidebar partial -------------------------------------------------------

// handleSidebarPartial serves GET /_partials/sidebar. The sidebar's
// htmx hx-get points here; the swap-target id is `#sidebar` so the
// outerHTML swap replaces the whole rail. Returning the partial directly
// (instead of going through Page → strip-chrome) avoids the chicken-and-egg
// where Page's HX-Request fall-through renders just the page body without
// any sidebar to select from.
//
// The partial reads chrome via a.Chrome(w, r) so the project list it renders
// matches what a fresh page render would show. Pass a sidebar-shaped struct
// (PageData with Chrome populated) so the template's `.Chrome.Projects` /
// `.CurrentSlug` references resolve.
func (a *app) handleSidebarPartial(w http.ResponseWriter, r *http.Request) {
	// CurrentSlug + URL aren't known from the partial endpoint itself (the
	// sidebar refresh is body-scoped). htmx sends the page URL via
	// HX-Current-URL on every triggered request — use it to keep the active
	// highlight + per-project nav stable across refreshes. Fall back to a
	// `?slug=` query for non-htmx callers.
	currentSlug := r.URL.Query().Get("slug")
	chrome := a.Chrome(w, r)
	if hxURL := r.Header.Get("HX-Current-URL"); hxURL != "" {
		if u, err := url.Parse(hxURL); err == nil {
			chrome.URL = u.Path
			if currentSlug == "" {
				currentSlug = slugFromPath(u.Path)
			}
		}
	}
	a.renderer.Partial(w, r, "sidebar", PageData{
		Chrome:      chrome,
		CurrentSlug: currentSlug,
	})
}

// slugFromPath extracts the project slug from a /p/{slug}/... URL path.
// Returns "" for paths that don't match. Used by the sidebar refresh to
// recover the active project context from htmx's HX-Current-URL.
func slugFromPath(path string) string {
	if !strings.HasPrefix(path, "/p/") {
		return ""
	}
	rest := strings.TrimPrefix(path, "/p/")
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	// Filter out the literal segments that aren't real slugs.
	switch rest {
	case "", "new", "load":
		return ""
	}
	return rest
}

// --- middleware ------------------------------------------------------------

// withCSRF wraps a POST handler with CSRF verification. For non-POST
// requests it passes through (CSRF only applies to state-changing verbs).
// On token mismatch returns 403 with an inline error partial.
//
// The session middleware must run before this one so SessionIDFrom is
// populated.
func (a *app) withCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			id, _ := svc.SessionIDFrom(r.Context())
			if err := checkCSRF(r, a.session.secret, id); err != nil {
				a.renderer.Error(w, r, http.StatusForbidden, err)
				return
			}
		}
		next(w, r)
	}
}
