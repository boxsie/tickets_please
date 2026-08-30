package web

import (
	"net/http"

	userspg "tickets_please/internal/web/components/pages/users"
)

// handleUserDetail serves GET /u/{id} — the profile of a registered human the
// agents act for. It's the destination of the "(for <user>)" attribution links
// on comments and ticket metadata. App-global (not project-scoped), login-gated
// like /agents.
func (a *app) handleUserDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	prof, err := a.deps.Service.GetUserProfile(r.Context(), id)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	rows := make([]userspg.MembershipRow, 0, len(prof.Memberships))
	for _, m := range prof.Memberships {
		rows = append(rows, userspg.MembershipRow{
			ProjectID:   m.ProjectID,
			ProjectSlug: m.ProjectSlug,
			ProjectName: m.ProjectName,
			Role:        string(m.Role),
		})
	}
	a.renderer.RenderTempl(w, r, PageOpts{
		Title: prof.User.DisplayName + " · tickets_please",
	}, userspg.Detail(userspg.DetailProps{
		User:        prof.User,
		Memberships: rows,
	}))
}
