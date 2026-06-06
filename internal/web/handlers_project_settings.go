package web

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
	"tickets_please/internal/vecindex"
	projectspg "tickets_please/internal/web/components/pages/projects"
)

// Per-project Settings page handlers — replaced the old /p/{slug}/edit form
// with a single page that bundles name+description editing with embedder
// config (provider/model) plus a CSRF'd Re-embed button.
//
// The status block reads the project's `summary.embedding.json` sidecar to
// surface the (provider, model, dim) actually written to disk and contrasts
// it with what the project record asks for. v1 doesn't try to count "stale"
// sidecars across the whole tree — that's a heavyweight walk; the framing
// "Re-embed if you changed the model" carries the same intent.

// projectSettingsData is the payload for pages/projects/settings.tmpl.
// FormError is the inline validation message for the settings POST. Status
// is the cluster of "what's on disk vs. what's configured" facts the page
// surfaces above the form.
type projectSettingsData struct {
	Project   *domain.Project
	FormError string
	Submitted projectSettingsSubmitted
	Status    embedStatus
	// Archive-policy fieldset + optional inline sweep report.
	Archive      svc.ArchivePolicy
	ArchiveError string
	Report       *svc.ApplyPolicyReport
	Committed    bool // the report is an apply (true) vs a dry-run (false)
	IsOwner      bool
}

// projectSettingsSubmitted captures the user-typed form values so a
// validation failure round-trips them back into the re-rendered form.
type projectSettingsSubmitted struct {
	Name          string
	Description   string
	EmbedProvider string
	EmbedModel    string
}

// embedStatus is the small "what's running, what's expected" panel above the
// form. SidecarPresent=false means the summary sidecar is missing entirely —
// either the project was just created or a re-embed is in flight.
type embedStatus struct {
	SidecarPresent   bool
	SidecarProvider  string
	SidecarModel     string
	SidecarDim       int
	ExpectedProvider string
	ExpectedModel    string
}

// readEmbedStatus reads the project's summary.embedding.json sidecar (if
// present) and pairs it with the (provider, model) the project record asks
// for. Errors are swallowed — the caller falls back to "no sidecars yet".
func (a *app) readEmbedStatus(ctx context.Context, slug string) embedStatus {
	out := embedStatus{}
	st, err := a.deps.Service.ResolveProjectStore(ctx, slug)
	if err != nil || st == nil {
		return out
	}
	rec, err := st.ReadProject(slug)
	if err == nil && rec != nil {
		out.ExpectedProvider = rec.EmbedProvider
		out.ExpectedModel = rec.EmbedModel
	}
	side := filepath.Join(st.Root, "summary.embedding.json")
	sc, err := vecindex.ReadSidecar(side)
	if err != nil {
		return out
	}
	out.SidecarPresent = true
	out.SidecarProvider = sc.Provider
	out.SidecarModel = sc.Model
	out.SidecarDim = sc.Dim
	return out
}

// handleProjectSettings serves GET /p/{slug}/settings — the consolidated
// project Settings page (name + description + embed_provider + embed_model
// + a Re-embed button + status block).
func (a *app) handleProjectSettings(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	status := a.readEmbedStatus(r.Context(), proj.Slug)
	policy, _ := a.deps.Service.GetArchivePolicy(r.Context(), proj.Slug)
	a.renderer.RenderTempl(w, r, PageOpts{
		Title:       "Settings · " + proj.Name + " · tickets_please",
		CurrentSlug: proj.Slug,
	}, projectspg.Settings(settingsToProps(projectSettingsData{
		Project: proj,
		Status:  status,
		Submitted: projectSettingsSubmitted{
			Name:          proj.Name,
			Description:   proj.Description,
			EmbedProvider: status.ExpectedProvider,
			EmbedModel:    status.ExpectedModel,
		},
		Archive: policy,
		IsOwner: true, // the settings route is already RoleOwner-gated
	}, a.summaryCSRF(r))))
}

// settingsToProps converts the web-package's projectSettingsData into the
// projects-package mirror.
func settingsToProps(d projectSettingsData, csrf string) projectspg.SettingsProps {
	return projectspg.SettingsProps{
		Project:   d.Project,
		FormError: d.FormError,
		Submitted: projectspg.SettingsSubmitted{
			Name:          d.Submitted.Name,
			Description:   d.Submitted.Description,
			EmbedProvider: d.Submitted.EmbedProvider,
			EmbedModel:    d.Submitted.EmbedModel,
		},
		Status: projectspg.EmbedStatus{
			SidecarPresent:   d.Status.SidecarPresent,
			SidecarProvider:  d.Status.SidecarProvider,
			SidecarModel:     d.Status.SidecarModel,
			SidecarDim:       d.Status.SidecarDim,
			ExpectedProvider: d.Status.ExpectedProvider,
			ExpectedModel:    d.Status.ExpectedModel,
		},
		CSRF: csrf,
		Archive: projectspg.ArchivePolicyView{
			Enabled:             d.Archive.Enabled,
			MinAgeDays:          d.Archive.MinAgeDays,
			MinRetrievals:       d.Archive.MinRetrievals,
			DislikeRatio:        d.Archive.DislikeRatio,
			EarlyArchiveAgeDays: d.Archive.EarlyArchiveAgeDays,
			AutoSweepOnMount:    d.Archive.AutoSweepOnMount,
		},
		ArchiveError: d.ArchiveError,
		IsOwner:      d.IsOwner,
		Report:       archiveReportView(d),
	}
}

// archiveReportView converts a svc sweep report into the page mirror. Returns
// nil when there's no report to show (the common case — only Preview/Apply set
// one). Dry-run reports carry WouldArchive; apply reports carry Archived.
func archiveReportView(d projectSettingsData) *projectspg.ArchiveReportView {
	if d.Report == nil {
		return nil
	}
	src := d.Report.WouldArchive
	if d.Committed {
		src = d.Report.Archived
	}
	rows := make([]projectspg.ArchiveReportRow, len(src))
	for i, e := range src {
		rows[i] = projectspg.ArchiveReportRow{
			TicketID: e.TicketID,
			Title:    e.Title,
			Reason:   e.Reason,
			Slug:     d.Project.Slug,
		}
	}
	return &projectspg.ArchiveReportView{
		Committed:  d.Committed,
		Considered: d.Report.Considered,
		Rows:       rows,
	}
}

// handleProjectSettingsUpdate handles POST /p/{slug}/settings. Calls
// Service.UpdateProject — when embed_* fields change, the service auto-fires
// ReembedProject after writing the yaml. Flash + redirect on success;
// inline error re-render on failure.
func (a *app) handleProjectSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	in := projectSettingsSubmitted{
		Name:          strings.TrimSpace(r.Form.Get("name")),
		Description:   r.Form.Get("description"),
		EmbedProvider: strings.TrimSpace(r.Form.Get("embed_provider")),
		EmbedModel:    strings.TrimSpace(r.Form.Get("embed_model")),
	}
	updateIn := domain.UpdateProjectInput{
		Name:          &in.Name,
		Description:   &in.Description,
		EmbedProvider: &in.EmbedProvider,
		EmbedModel:    &in.EmbedModel,
	}
	if _, err := a.deps.Service.UpdateProject(r.Context(), slug, updateIn); err != nil {
		status := classifyServiceError(err)
		w.WriteHeader(status)
		a.renderer.RenderTempl(w, r, PageOpts{
			Title:       "Settings · " + proj.Name + " · tickets_please",
			CurrentSlug: proj.Slug,
		}, projectspg.Settings(settingsToProps(projectSettingsData{
			Project:   proj,
			FormError: err.Error(),
			Submitted: in,
			Status:    a.readEmbedStatus(r.Context(), proj.Slug),
		}, a.summaryCSRF(r))))
		return
	}
	SetFlash(w, r, "success", "Project settings saved.")
	http.Redirect(w, r, "/p/"+proj.Slug+"/settings", http.StatusSeeOther)
}

// handleArchivePolicy serves POST /p/{slug}/archive-policy. One form, three
// actions: `save` persists the six knobs via svc.SetArchivePolicy; `preview`
// and `apply` run svc.ApplyArchivePolicy (commit=false / true) and re-render
// the settings page with the sweep report inline. Preview/Apply run against
// the *saved* policy (apply reads mount state), so after them the form shows
// the saved values — the hint tells the user to save before previewing.
func (a *app) handleArchivePolicy(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	action := strings.TrimSpace(r.Form.Get("action"))

	switch action {
	case "save":
		policy := parseArchiveForm(r)
		if _, err := a.deps.Service.SetArchivePolicy(r.Context(), slug, policy); err != nil {
			a.renderArchiveSettings(w, r, proj, policy, nil, false, err.Error(), classifyServiceError(err))
			return
		}
		SetFlash(w, r, "success", "Archive policy saved.")
		http.Redirect(w, r, "/p/"+slug+"/settings", http.StatusSeeOther)
	case "preview", "apply":
		commit := action == "apply"
		report, err := a.deps.Service.ApplyArchivePolicy(r.Context(), svc.ApplyPolicyInput{
			ProjectIDOrSlug: slug,
			Commit:          commit,
		})
		// Show the saved policy in the form regardless (preview/apply act on it).
		saved, _ := a.deps.Service.GetArchivePolicy(r.Context(), slug)
		if err != nil {
			a.renderArchiveSettings(w, r, proj, saved, nil, false, err.Error(), classifyServiceError(err))
			return
		}
		a.renderArchiveSettings(w, r, proj, saved, report, commit, "", http.StatusOK)
	default:
		a.renderer.RenderTemplError(w, r, http.StatusBadRequest, errors.New("unknown action (expected save|preview|apply)"))
	}
}

// parseArchiveForm pulls the six archive knobs off the POST form. Checkboxes
// are true exactly when present; numbers default to 0 on a parse miss (the
// number inputs have min=0 so an empty field reads as 0).
func parseArchiveForm(r *http.Request) svc.ArchivePolicy {
	atoi := func(k string) int { n, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get(k))); return n }
	ratio, _ := strconv.ParseFloat(strings.TrimSpace(r.Form.Get("dislike_ratio")), 64)
	return svc.ArchivePolicy{
		Enabled:             r.Form.Get("enabled") == "true",
		MinAgeDays:          atoi("min_age_days"),
		MinRetrievals:       atoi("min_retrievals"),
		DislikeRatio:        ratio,
		EarlyArchiveAgeDays: atoi("early_archive_age_days"),
		AutoSweepOnMount:    r.Form.Get("auto_sweep_on_mount") == "true",
	}
}

// renderArchiveSettings re-renders the full settings page with the archive
// fieldset reflecting `policy`, an optional sweep `report`, and an optional
// inline error. Used by every non-redirect path of handleArchivePolicy.
func (a *app) renderArchiveSettings(w http.ResponseWriter, r *http.Request, proj *domain.Project, policy svc.ArchivePolicy, report *svc.ApplyPolicyReport, committed bool, archiveErr string, status int) {
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	st := a.readEmbedStatus(r.Context(), proj.Slug)
	a.renderer.RenderTempl(w, r, PageOpts{
		Title:       "Settings · " + proj.Name + " · tickets_please",
		CurrentSlug: proj.Slug,
	}, projectspg.Settings(settingsToProps(projectSettingsData{
		Project: proj,
		Status:  st,
		Submitted: projectSettingsSubmitted{
			Name:          proj.Name,
			Description:   proj.Description,
			EmbedProvider: st.ExpectedProvider,
			EmbedModel:    st.ExpectedModel,
		},
		Archive:      policy,
		ArchiveError: archiveErr,
		Report:       report,
		Committed:    committed,
		IsOwner:      true,
	}, a.summaryCSRF(r))))
}

// handleProjectReembed handles POST /p/{slug}/reembed. Fires the explicit
// wipe-and-rebuild path. The button's hx-confirm intercepts the click on the
// browser side; CSRF is checked by the wrap middleware before we run.
//
// On failure (typically a probe error: the project's embed_model isn't pulled
// in Ollama yet) we flash the verbatim error and redirect back to the
// settings page so the user can see what went wrong without losing form
// context. This matches the UpdateProject path: the mount's existing
// embedder is still live, only the swap was refused.
func (a *app) handleProjectReembed(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if err := a.deps.Service.ReembedProject(r.Context(), slug); err != nil {
		SetFlash(w, r, "error", "Re-embed failed: "+err.Error())
		http.Redirect(w, r, "/p/"+slug+"/settings", http.StatusSeeOther)
		return
	}
	SetFlash(w, r, "success", "Re-embed enqueued for "+slug+".")
	http.Redirect(w, r, "/p/"+slug+"/settings", http.StatusSeeOther)
}
