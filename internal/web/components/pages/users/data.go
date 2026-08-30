// Package users hosts the /u/{id} user-profile page — the human an agent acts
// for. It's the destination of the "(for Dan)" attribution links. Like the
// other page packages it declares mirror prop types so it never imports web or
// svc (avoiding an import cycle).
package users

import "tickets_please/internal/domain"

// DetailProps is the payload for the user-profile page.
type DetailProps struct {
	User        *domain.User
	Memberships []MembershipRow
}

// MembershipRow is one project the user belongs to. ProjectID is the fallback
// label when the project isn't currently mounted (slug/name unknown).
type MembershipRow struct {
	ProjectID   string
	ProjectSlug string
	ProjectName string
	Role        string
}

// providerLogin returns a short "provider: handle" label for whichever OAuth
// identity the user has linked, or "" when none is set.
func providerLogin(u *domain.User) string {
	if u == nil {
		return ""
	}
	if u.GitHubLogin != nil && *u.GitHubLogin != "" {
		return "github: " + *u.GitHubLogin
	}
	if u.GoogleSub != nil && *u.GoogleSub != "" {
		return "google"
	}
	return ""
}

// displayName falls back to the email, then an id stub, so the header always
// has something to show.
func displayName(u *domain.User) string {
	if u == nil {
		return "unknown user"
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	if u.Email != "" {
		return u.Email
	}
	if len(u.ID) > 8 {
		return u.ID[:8]
	}
	return u.ID
}
