package web

import (
	"errors"
	"net/http"
	"strings"

	"tickets_please/internal/auth"
	"tickets_please/internal/domain"
	memberspg "tickets_please/internal/web/components/pages/members"
)

// Members + invitations UI (#077, lean cut — no SMTP). Five owner-only routes
// under /p/{slug}/members plus the public-ish accept route:
//
//	GET  /p/{slug}/members                              — list + invite form
//	POST /p/{slug}/members/invite                       — mint a token invite
//	POST /p/{slug}/members/{user_id}/role              — change a member's role
//	POST /p/{slug}/members/{user_id}/remove            — revoke membership
//	POST /p/{slug}/members/invitations/{id}/cancel     — drop a pending invite
//	GET  /invite/{token}                                — accept (login-gated)
//
// The owner-only routes are wrapped with slugRole(domain.RoleOwner, ...); the
// accept route is wrapped with `authed` so an anonymous visitor is bounced to
// login (?next= back here) and returns once signed in.

// invitePath builds the inline shareable accept link for a token. Lives at
// /invite/{token} (not under /auth/) to avoid the /auth/{provider} wildcard.
func invitePath(token string) string { return "/invite/" + token }

// currentUserID returns the logged-in user's id, or "" (auth-disabled mode or
// not signed in).
func currentUserID(r *http.Request) string {
	if u, ok := auth.UserFrom(r.Context()); ok {
		return u.ID
	}
	return ""
}

func (a *app) handleMembersIndex(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	proj, err := a.deps.Service.GetProject(r.Context(), slug)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	members, err := a.deps.Service.ListProjectMembers(r.Context(), slug)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	invites, err := a.deps.Service.ListInvitations(r.Context(), slug)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}

	me := currentUserID(r)
	rows := make([]memberspg.MemberRow, 0, len(members))
	for _, m := range members {
		rows = append(rows, memberspg.MemberRow{
			UserID:      m.UserID,
			DisplayName: m.DisplayName,
			Email:       m.Email,
			Role:        m.Role,
			IsSelf:      m.UserID == me && me != "",
		})
	}
	inviteRows := make([]memberspg.InviteRow, 0, len(invites))
	for _, inv := range invites {
		inviteRows = append(inviteRows, memberspg.InviteRow{
			ID:         inv.ID,
			Email:      inv.Email,
			Role:       inv.Role,
			AcceptPath: invitePath(inv.Token),
			ExpiresAt:  inv.ExpiresAt,
		})
	}

	a.renderer.RenderTempl(w, r, PageOpts{
		Title:       "Members · " + proj.Name + " · tickets_please",
		CurrentSlug: proj.Slug,
	}, memberspg.Index(memberspg.IndexProps{
		Project:     proj,
		Members:     rows,
		Invitations: inviteRows,
		CSRF:        a.summaryCSRF(r),
	}))
}

func (a *app) handleMemberInvite(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	email := strings.TrimSpace(r.Form.Get("email"))
	role := domain.Role(strings.TrimSpace(r.Form.Get("role")))
	if _, err := a.deps.Service.CreateInvitation(r.Context(), slug, email, role, currentUserID(r)); err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	SetFlash(w, r, "success", "Invitation created — copy the link below to share it.")
	http.Redirect(w, r, "/p/"+slug+"/members", http.StatusSeeOther)
}

func (a *app) handleMemberRole(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	userID := r.PathValue("user_id")
	role := domain.Role(strings.TrimSpace(r.Form.Get("role")))
	// Guard against an owner demoting themselves and losing access to this
	// very page — they'd be unable to undo it.
	if me := currentUserID(r); me != "" && me == userID && role != domain.RoleOwner {
		a.renderer.RenderTemplError(w, r, http.StatusUnprocessableEntity,
			errors.New("you can't change your own role — ask another owner"))
		return
	}
	if err := a.deps.Service.SetMemberRole(r.Context(), slug, userID, role, currentUserID(r)); err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	SetFlash(w, r, "success", "Role updated.")
	http.Redirect(w, r, "/p/"+slug+"/members", http.StatusSeeOther)
}

func (a *app) handleMemberRemove(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	userID := r.PathValue("user_id")
	if me := currentUserID(r); me != "" && me == userID {
		a.renderer.RenderTemplError(w, r, http.StatusUnprocessableEntity,
			errors.New("you can't remove yourself — ask another owner"))
		return
	}
	if err := a.deps.Service.RemoveMember(r.Context(), slug, userID); err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	SetFlash(w, r, "success", "Member removed.")
	http.Redirect(w, r, "/p/"+slug+"/members", http.StatusSeeOther)
}

func (a *app) handleInviteCancel(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	invID := r.PathValue("id")
	if err := a.deps.Service.CancelInvitation(r.Context(), slug, invID); err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	SetFlash(w, r, "success", "Invitation cancelled.")
	http.Redirect(w, r, "/p/"+slug+"/members", http.StatusSeeOther)
}

// handleInviteAccept serves GET /auth/invite/{token}. The `authed` wrapper has
// already bounced anonymous visitors to login (and back); here we just consume
// the token and land the now-member on the project.
func (a *app) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	me := currentUserID(r)
	if me == "" {
		// Auth-disabled localhost mode: there's no user to grant to.
		a.renderer.RenderTemplError(w, r, http.StatusForbidden,
			errors.New("accepting an invitation requires signing in (configure an OAuth provider)"))
		return
	}
	inv, err := a.deps.Service.AcceptInvitation(r.Context(), token, me)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	loc := "/"
	if proj, perr := a.deps.Service.GetProject(r.Context(), inv.ProjectID); perr == nil {
		loc = "/p/" + proj.Slug
		SetFlash(w, r, "success", "You've joined "+proj.Name+".")
	} else {
		SetFlash(w, r, "success", "Invitation accepted.")
	}
	http.Redirect(w, r, loc, http.StatusSeeOther)
}
