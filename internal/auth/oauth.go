// Package auth defines the OAuth provider abstraction used by the web UI's
// login flow. Providers are deliberately tiny and stateless — they turn an
// authorization code into a normalized set of identity Claims. Everything
// about sessions, cookies, and user persistence lives in the web layer; this
// package knows nothing about HTTP handlers or the store.
//
// The small surface (Name / AuthorizeURL / Exchange) is what lets future
// providers — passkeys, Tailscale, magic-link — plug in without touching the
// callback handler.
package auth

import "context"

// Claims is the normalized identity a provider returns after a successful
// token exchange. Subject is the STABLE per-provider identifier the user store
// keys on: for GitHub that's the login (matching the User.GitHubLogin field),
// for Google the OIDC `sub`. Email/DisplayName/AvatarURL are best-effort
// profile fields used to populate or refresh the User record.
type Claims struct {
	Provider    string
	Subject     string
	Email       string
	DisplayName string
	AvatarURL   string
}

// Provider is one OAuth identity source. Implementations are constructed once
// at startup from config and are safe for concurrent use.
type Provider interface {
	// Name is the lowercase provider key ("github", "google") used in route
	// paths and as the store's OAuth-subject provider discriminator.
	Name() string

	// AuthorizeURL returns the provider's consent-screen URL for the given
	// CSRF state and the callback redirectURL. The caller is responsible for
	// generating + persisting state (in a signed short-lived cookie) and for
	// constructing redirectURL as <base>/auth/<name>/callback.
	AuthorizeURL(state, redirectURL string) string

	// Exchange swaps the authorization code for an access token and fetches
	// the user's profile, returning normalized Claims. redirectURL MUST match
	// the value passed to AuthorizeURL (the provider validates it).
	Exchange(ctx context.Context, code, redirectURL string) (*Claims, error)
}
