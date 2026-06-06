// Package members hosts the templ page for the per-project members + pending
// invitations management screen (/p/{slug}/members, owner-only).
//
// As with the other page packages, the prop types here are structural mirrors
// the handler fills at the render boundary — kept local so the components don't
// import the web/svc packages (which would cycle).
package members

import (
	"time"

	"tickets_please/internal/domain"
)

// IndexProps drives the members page.
type IndexProps struct {
	Project     *domain.Project
	Members     []MemberRow
	Invitations []InviteRow
	CSRF        string
}

// MemberRow is one current member: identity + role, with IsSelf flagging the
// acting owner (their row hides the Remove action and labels them "you").
type MemberRow struct {
	UserID      string
	DisplayName string
	Email       string
	Role        domain.Role
	IsSelf      bool
}

// InviteRow is one pending invitation. AcceptPath is the inline shareable
// /auth/invite/{token} link (no SMTP in this build — the owner copies it).
type InviteRow struct {
	ID         string
	Email      string
	Role       domain.Role
	AcceptPath string
	ExpiresAt  time.Time
}

// roleOptions is the role dropdown order, broadest first.
var roleOptions = []domain.Role{domain.RoleOwner, domain.RoleMember, domain.RoleViewer}

// membersHref / inviteHref / roleHref / removeHref / cancelHref build the form
// action URLs for the page. Centralised here so the templ stays declarative.
func membersHref(slug string) string { return "/p/" + slug + "/members" }
func inviteHref(slug string) string  { return "/p/" + slug + "/members/invite" }
func roleHref(slug, userID string) string {
	return "/p/" + slug + "/members/" + userID + "/role"
}
func removeHref(slug, userID string) string {
	return "/p/" + slug + "/members/" + userID + "/remove"
}
func cancelHref(slug, invitationID string) string {
	return "/p/" + slug + "/members/invitations/" + invitationID + "/cancel"
}

func roleLabel(r domain.Role) string { return string(r) }

func emailOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func expiresLabel(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("Jan 2, 2006")
}
