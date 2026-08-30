package providers

import (
	"context"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"

	"tickets_please/internal/auth"
)

// gitHubEndpoint is the OAuth authorize/token pair for github.com.
var gitHubEndpoint = oauth2.Endpoint{
	AuthURL:  "https://github.com/login/oauth/authorize",
	TokenURL: "https://github.com/login/oauth/access_token",
}

// GitHub implements auth.Provider against github.com. Subject is the user's
// login (matching User.GitHubLogin) — the ticket-074 store keys on login, not
// the numeric id; rename-resilience can switch to the numeric id later without
// touching the callback flow.
type GitHub struct{ base }

// NewGitHub builds a GitHub provider for the given OAuth app credentials.
func NewGitHub(clientID, clientSecret string, opts ...Option) *GitHub {
	g := &GitHub{base{
		cfg: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Scopes:       []string{"read:user", "user:email"},
			Endpoint:     gitHubEndpoint,
		},
		apiBase: "https://api.github.com",
	}}
	for _, o := range opts {
		o(&g.base)
	}
	return g
}

// Name implements auth.Provider.
func (g *GitHub) Name() string { return "github" }

// AuthorizeURL implements auth.Provider.
func (g *GitHub) AuthorizeURL(state, redirectURL string) string {
	return g.authorizeURL(state, redirectURL)
}

// Exchange implements auth.Provider: code → token → /user (+ /user/emails when
// the primary email is private) → normalized Claims.
func (g *GitHub) Exchange(ctx context.Context, code, redirectURL string) (*auth.Claims, error) {
	client, err := g.exchangeToken(ctx, code, redirectURL)
	if err != nil {
		return nil, fmt.Errorf("github: exchange code: %w", err)
	}

	var profile struct {
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := getJSON(ctx, client, g.apiBase+"/user", &profile); err != nil {
		return nil, fmt.Errorf("github: fetch profile: %w", err)
	}
	if profile.Login == "" {
		return nil, fmt.Errorf("github: profile missing login")
	}

	email := profile.Email
	if email == "" {
		email = g.primaryEmail(ctx, client) // best-effort
	}
	name := profile.Name
	if name == "" {
		name = profile.Login
	}

	return &auth.Claims{
		Provider:    "github",
		Subject:     profile.Login,
		Email:       email,
		DisplayName: name,
		AvatarURL:   profile.AvatarURL,
	}, nil
}

// primaryEmail fetches the user's verified primary email when /user didn't
// expose one (the common case for users who keep their email private). Returns
// "" on any error — email is a nice-to-have, not a hard requirement.
func (g *GitHub) primaryEmail(ctx context.Context, client *http.Client) string {
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, client, g.apiBase+"/user/emails", &emails); err != nil {
		return ""
	}
	var firstVerified string
	for _, e := range emails {
		if e.Verified && firstVerified == "" {
			firstVerified = e.Email
		}
		if e.Primary && e.Verified {
			return e.Email
		}
	}
	return firstVerified
}
