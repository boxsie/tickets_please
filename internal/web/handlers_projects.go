package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

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
	currentSlug := ""
	if existing != nil {
		currentSlug = existing.Slug
	}
	a.renderer.Page(w, r, page, PageOpts{
		Title:       titleForFormError(mode),
		CurrentSlug: currentSlug,
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

// projectDetailData is the dashboard payload for pages/projects/detail.tmpl.
// The Overview page is the human-facing "state of play" — metrics, ready
// work, recent activity, recent learnings. The Summary view at
// /p/{slug}/summary is the LLM-loadable canonical doc and stays separate.
type projectDetailData struct {
	Project          *domain.Project
	Phases           []*domain.Phase
	Metrics          dashboardMetrics
	StatusSegments   []statusSegment
	ReadyTickets     []*domain.Ticket
	RecentActivity   []activityItem
	RecentLearnings  []learningExcerpt
}

// dashboardMetrics is the row of stat cards at the top of the dashboard.
type dashboardMetrics struct {
	Total      int
	Active     int
	InProgress int
	Done       int
}

// statusSegment is one slice of the horizontal stacked bar showing
// ticket distribution across columns.
type statusSegment struct {
	Column  domain.Column
	Label   string
	Count   int
	Percent int // 0-100, integer for clean width: %d%% style values
}

// activityItem describes one row in the "Recent activity" list. The
// underlying source is a ticket sorted by UpdatedAt desc — comments
// would require a per-ticket walk we don't want on every dashboard load.
type activityItem struct {
	Ticket *domain.Ticket
	Ago    string
}

// learningExcerpt is one row in the "Recent learnings" section. The
// excerpt is the first line of the learnings field, capped to keep the
// dashboard skim-friendly.
type learningExcerpt struct {
	Ticket  *domain.Ticket
	Excerpt string
	Ago     string
}

// handleProjectDetail serves GET /p/{slug} — the project dashboard.
// Section navigation (Board / Phases / Waves / Summary) lives in the
// sidebar's per-project nav.
func (a *app) handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.Error(w, r, classifyServiceError(err), err)
		return
	}
	phases, _ := a.deps.Service.ListPhases(r.Context(), proj.Slug)

	// One bulk read of every ticket in the project; the dashboard derives
	// every metric from this slice. listTickets caps at 200 by default —
	// fine for typical project sizes; large projects get a representative
	// sample, the board shows the rest.
	tickets, _, err := a.deps.Service.ListTickets(r.Context(), domain.ListTicketsInput{
		ProjectIDOrSlug: slug,
		Limit:           200,
	})
	if err != nil {
		// Best-effort: an error here degrades the dashboard to "no metrics"
		// rather than 500ing the whole page.
		a.deps.Logger.Warn("dashboard: list tickets", "err", err)
		tickets = nil
	}

	data := projectDetailData{
		Project:         proj,
		Phases:          phases,
		Metrics:         computeMetrics(tickets),
		StatusSegments:  computeStatusSegments(tickets),
		ReadyTickets:    pickReady(tickets, 5),
		RecentActivity:  pickRecentActivity(tickets, 10),
		RecentLearnings: pickRecentLearnings(tickets, 3),
	}

	a.renderer.Page(w, r, "projects/detail", PageOpts{
		Title:       proj.Name + " · tickets_please",
		CurrentSlug: proj.Slug,
		Body:        data,
	})
}

// computeMetrics tallies the four headline stat-card numbers. Active
// excludes done; InProgress is just the in_progress column.
func computeMetrics(tickets []*domain.Ticket) dashboardMetrics {
	m := dashboardMetrics{Total: len(tickets)}
	for _, t := range tickets {
		switch t.Column {
		case domain.ColumnTodo, domain.ColumnTesting:
			m.Active++
		case domain.ColumnInProgress:
			m.Active++
			m.InProgress++
		case domain.ColumnDone:
			m.Done++
		}
	}
	return m
}

// computeStatusSegments builds the four-segment horizontal bar.
// Percentages are rounded down so they sum to <=100; the template renders
// segments with a width: <p>% style. An empty project yields zero-count
// segments which the template hides.
func computeStatusSegments(tickets []*domain.Ticket) []statusSegment {
	cols := []struct {
		col   domain.Column
		label string
	}{
		{domain.ColumnTodo, "To do"},
		{domain.ColumnInProgress, "In progress"},
		{domain.ColumnTesting, "Testing"},
		{domain.ColumnDone, "Done"},
	}
	out := make([]statusSegment, 0, len(cols))
	total := len(tickets)
	for _, c := range cols {
		count := 0
		for _, t := range tickets {
			if t.Column == c.col {
				count++
			}
		}
		percent := 0
		if total > 0 {
			percent = (count * 100) / total
		}
		out = append(out, statusSegment{Column: c.col, Label: c.label, Count: count, Percent: percent})
	}
	return out
}

// pickReady returns up to n unblocked tickets in todo or in_progress,
// sorted by CreatedAt asc (oldest first → things that have been sitting
// without progress get surfaced).
func pickReady(tickets []*domain.Ticket, n int) []*domain.Ticket {
	out := make([]*domain.Ticket, 0, n)
	for _, t := range tickets {
		if t.Column != domain.ColumnTodo && t.Column != domain.ColumnInProgress {
			continue
		}
		if len(t.BlockedBy) > 0 {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// pickRecentActivity returns the n most-recently-updated tickets with a
// pre-rendered "N ago" relative-time label. Today this is "what changed
// recently"; a richer per-comment feed could come later via a service-
// level activity log.
func pickRecentActivity(tickets []*domain.Ticket, n int) []activityItem {
	src := make([]*domain.Ticket, len(tickets))
	copy(src, tickets)
	sort.Slice(src, func(i, j int) bool { return src[i].UpdatedAt.After(src[j].UpdatedAt) })
	if len(src) > n {
		src = src[:n]
	}
	out := make([]activityItem, len(src))
	for i, t := range src {
		out[i] = activityItem{Ticket: t, Ago: humanizeAgo(t.UpdatedAt)}
	}
	return out
}

// pickRecentLearnings surfaces the last n completion learnings as the
// "wisdom-at-a-glance" section. Excerpt is the first non-empty line of
// the learnings field, capped to ~140 chars.
func pickRecentLearnings(tickets []*domain.Ticket, n int) []learningExcerpt {
	src := make([]*domain.Ticket, 0, len(tickets))
	for _, t := range tickets {
		if t.Column == domain.ColumnDone && t.Learnings != nil && strings.TrimSpace(*t.Learnings) != "" {
			src = append(src, t)
		}
	}
	sort.Slice(src, func(i, j int) bool {
		ai, aj := src[i].CompletedAt, src[j].CompletedAt
		if ai == nil {
			return false
		}
		if aj == nil {
			return true
		}
		return ai.After(*aj)
	})
	if len(src) > n {
		src = src[:n]
	}
	out := make([]learningExcerpt, len(src))
	for i, t := range src {
		excerpt := firstLine(*t.Learnings, 140)
		ago := ""
		if t.CompletedAt != nil {
			ago = humanizeAgo(*t.CompletedAt)
		}
		out[i] = learningExcerpt{Ticket: t, Excerpt: excerpt, Ago: ago}
	}
	return out
}

// humanizeAgo formats a past time as "N <unit> ago" — "just now", "5m
// ago", "2h ago", "3d ago", "2w ago". Future times collapse to "just
// now" since they shouldn't happen on real data.
func humanizeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/24/7))
	default:
		return t.Format("2006-01-02")
	}
}

// firstLine returns the first non-blank line of s, trimmed and capped to
// max runes (with an ellipsis if truncated).
func firstLine(s string, max int) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len([]rune(line)) > max {
			return string([]rune(line)[:max-1]) + "…"
		}
		return line
	}
	return ""
}

// handleProjectUpdate handles POST /p/{slug} — name + description only.
// Slug is immutable; summary lives behind the dedicated summary editor.
// Kept around for back-compat with htmx in-place editors / the
// `update_project` MCP-adjacent flow; the Settings page (/p/{slug}/settings)
// is the new primary editor.
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
		// Back-compat path — htmx in-place editors and update_project. We no
		// longer carry a dedicated edit form template; surface validation
		// errors via the standard error partial.
		a.renderer.Error(w, r, classifyServiceError(err), err)
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
