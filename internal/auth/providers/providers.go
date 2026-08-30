// Package providers implements the auth.Provider interface for concrete OAuth
// identity sources. Each provider wraps a golang.org/x/oauth2 Config and a
// profile-fetch step that normalizes the provider's user payload into
// auth.Claims.
//
// Endpoints are hardcoded as oauth2.Endpoint literals rather than pulled from
// golang.org/x/oauth2/{github,google} so the dependency graph stays at a single
// module (the google subpackage drags in cloud.google.com/go/compute/metadata).
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
)

// base is the shared OAuth machinery embedded by each concrete provider.
// apiBase is the profile-fetch root (provider-specific meaning: GitHub's API
// root vs Google's full userinfo URL). client is an optional override used by
// tests to redirect HTTP at an httptest server.
type base struct {
	cfg     oauth2.Config
	apiBase string
	client  *http.Client
}

// Option tweaks a provider after construction. The exported options exist
// primarily as test seams (point the token + profile endpoints at a stub).
type Option func(*base)

// WithHTTPClient overrides the HTTP client used for both the token exchange
// and the profile fetch.
func WithHTTPClient(c *http.Client) Option { return func(b *base) { b.client = c } }

// WithEndpoint overrides the OAuth authorize/token endpoint.
func WithEndpoint(e oauth2.Endpoint) Option { return func(b *base) { b.cfg.Endpoint = e } }

// WithAPIBase overrides the profile-fetch base URL.
func WithAPIBase(u string) Option { return func(b *base) { b.apiBase = u } }

// authorizeURL builds the consent-screen URL with a per-call redirect.
func (b *base) authorizeURL(state, redirectURL string) string {
	c := b.cfg
	c.RedirectURL = redirectURL
	return c.AuthCodeURL(state)
}

// exchangeToken swaps the code for a token and returns an *http.Client that
// injects the bearer token on subsequent requests. A test-supplied http client
// is threaded through the oauth2 context so both legs hit the stub server.
func (b *base) exchangeToken(ctx context.Context, code, redirectURL string) (*http.Client, error) {
	c := b.cfg
	c.RedirectURL = redirectURL
	if b.client != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, b.client)
	}
	tok, err := c.Exchange(ctx, code)
	if err != nil {
		return nil, err
	}
	return c.Client(ctx, tok), nil
}

// getJSON does an authenticated GET and decodes a JSON body into dst.
func getJSON(ctx context.Context, client *http.Client, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("GET %s: unexpected status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}
