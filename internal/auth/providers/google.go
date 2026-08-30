package providers

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"

	"tickets_please/internal/auth"
)

// googleEndpoint is the OAuth authorize/token pair for Google. Literal copy of
// golang.org/x/oauth2/google.Endpoint (avoids importing that subpackage and
// its cloud-metadata transitive).
var googleEndpoint = oauth2.Endpoint{
	AuthURL:  "https://accounts.google.com/o/oauth2/auth",
	TokenURL: "https://oauth2.googleapis.com/token",
}

// Google implements auth.Provider against Google's OpenID Connect. Subject is
// the OIDC `sub` claim (stable across email/name changes). For Google the
// embedded base.apiBase holds the FULL userinfo URL (overridable in tests via
// WithAPIBase), since there's no second path to append.
type Google struct{ base }

// NewGoogle builds a Google provider for the given OAuth app credentials.
func NewGoogle(clientID, clientSecret string, opts ...Option) *Google {
	g := &Google{base{
		cfg: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint:     googleEndpoint,
		},
		apiBase: "https://openidconnect.googleapis.com/v1/userinfo",
	}}
	for _, o := range opts {
		o(&g.base)
	}
	return g
}

// Name implements auth.Provider.
func (g *Google) Name() string { return "google" }

// AuthorizeURL implements auth.Provider.
func (g *Google) AuthorizeURL(state, redirectURL string) string {
	return g.authorizeURL(state, redirectURL)
}

// Exchange implements auth.Provider: code → token → userinfo → Claims.
func (g *Google) Exchange(ctx context.Context, code, redirectURL string) (*auth.Claims, error) {
	client, err := g.exchangeToken(ctx, code, redirectURL)
	if err != nil {
		return nil, fmt.Errorf("google: exchange code: %w", err)
	}

	var info struct {
		Sub     string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := getJSON(ctx, client, g.apiBase, &info); err != nil {
		return nil, fmt.Errorf("google: fetch userinfo: %w", err)
	}
	if info.Sub == "" {
		return nil, fmt.Errorf("google: userinfo missing sub")
	}

	name := info.Name
	if name == "" {
		name = info.Email
	}

	return &auth.Claims{
		Provider:    "google",
		Subject:     info.Sub,
		Email:       info.Email,
		DisplayName: name,
		AvatarURL:   info.Picture,
	}, nil
}
