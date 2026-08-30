package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"tickets_please/internal/auth"
	"tickets_please/internal/svc"
)

// fakeProvider is a stub auth.Provider that returns canned claims without any
// network round-trip — the seam the ticket's test plan calls for.
type fakeProvider struct {
	name         string
	claims       *auth.Claims
	lastCode     string
	lastRedirect string
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) AuthorizeURL(state, redirectURL string) string {
	return "https://provider.example/authorize?state=" + url.QueryEscape(state) +
		"&redirect_uri=" + url.QueryEscape(redirectURL)
}

func (f *fakeProvider) Exchange(_ context.Context, code, redirectURL string) (*auth.Claims, error) {
	f.lastCode = code
	f.lastRedirect = redirectURL
	return f.claims, nil
}

// newAuthTestServer builds a server with only the auth routes mounted and the
// supplied provider injected (bypassing config-driven buildProviders so tests
// don't need real OAuth credentials).
func newAuthTestServer(t *testing.T, prov auth.Provider) (*httptest.Server, *app, *svc.Service) {
	t.Helper()
	deps := freshDeps(t)
	a := newApp(deps)
	a.providers = map[string]auth.Provider{prov.Name(): prov}

	wrap := func(h http.HandlerFunc) http.Handler {
		return a.session.middleware(http.HandlerFunc(a.withCSRF(h)))
	}
	mux := http.NewServeMux()
	mux.Handle("GET /auth/login", wrap(a.handleLoginPage))
	mux.Handle("GET /auth/{provider}/start", wrap(a.handleAuthStart))
	mux.Handle("GET /auth/{provider}/callback", wrap(a.handleAuthCallback))
	mux.Handle("POST /auth/logout", wrap(a.handleLogout))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, a, deps.Service
}

// noRedirectClient returns a client with a cookie jar that captures redirects
// instead of following them, so tests can inspect Location + Set-Cookie.
func noRedirectClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func TestAuthFlow_StartCallbackUpsertsUserAndAuthenticates(t *testing.T) {
	prov := &fakeProvider{
		name: "github",
		claims: &auth.Claims{
			Provider:    "github",
			Subject:     "boxsie",
			Email:       "dan@example.com",
			DisplayName: "Dan",
			AvatarURL:   "https://example.com/dan.png",
		},
	}
	srv, a, s := newAuthTestServer(t, prov)
	client := noRedirectClient(t)
	base, _ := url.Parse(srv.URL)

	// 1. /auth/github/start → 303 to provider, sets the signed state cookie.
	resp, err := client.Get(srv.URL + "/auth/github/start?next=" + url.QueryEscape("/p"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("start status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "https://provider.example/authorize") {
		t.Fatalf("start Location = %q, want provider authorize URL", loc)
	}

	// Recover the state value the server stashed in the tp_oauth cookie.
	var state string
	for _, c := range client.Jar.Cookies(base) {
		if c.Name == oauthStateCookie {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(c)
			gotProvider, gotState, gotTarget, ok := a.readStateCookie(r)
			if !ok {
				t.Fatal("state cookie failed to verify")
			}
			if gotProvider != "github" || gotTarget != "/p" {
				t.Fatalf("state cookie = (%q,%q), want (github,/p)", gotProvider, gotTarget)
			}
			state = gotState
		}
	}
	if state == "" {
		t.Fatal("no tp_oauth state cookie was set")
	}

	// 2. /auth/github/callback?state=...&code=... → upsert + session cookie + 303 to /p.
	resp, err = client.Get(srv.URL + "/auth/github/callback?state=" + url.QueryEscape(state) + "&code=abc123")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/p" {
		t.Fatalf("callback Location = %q, want /p", loc)
	}
	if prov.lastCode != "abc123" {
		t.Errorf("provider got code %q, want abc123", prov.lastCode)
	}

	// User was upserted in the store, keyed by OAuth subject.
	rec, err := s.UserStore.FindUserByOAuthSubject("github", "boxsie")
	if err != nil {
		t.Fatalf("FindUserByOAuthSubject: %v", err)
	}
	if rec.Email != "dan@example.com" || rec.DisplayName != "Dan" {
		t.Errorf("upserted user = %+v", rec)
	}
	if rec.GitHubLogin == nil || *rec.GitHubLogin != "boxsie" {
		t.Errorf("GitHubLogin not linked: %+v", rec.GitHubLogin)
	}

	// 3. Next request is authenticated: the tp_user cookie carries the user id.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range client.Jar.Cookies(base) {
		r.AddCookie(c)
	}
	if got := a.userIDFromCookie(r); got != rec.ID {
		t.Fatalf("userIDFromCookie = %q, want %q", got, rec.ID)
	}
}

func TestAuthCallback_RejectsStateMismatch(t *testing.T) {
	prov := &fakeProvider{name: "github", claims: &auth.Claims{Provider: "github", Subject: "x"}}
	srv, _, _ := newAuthTestServer(t, prov)
	client := noRedirectClient(t)

	// Prime a state cookie via start, then send the wrong state to callback.
	if resp, err := client.Get(srv.URL + "/auth/github/start"); err != nil {
		t.Fatalf("start: %v", err)
	} else {
		_ = resp.Body.Close()
	}
	resp, err := client.Get(srv.URL + "/auth/github/callback?state=WRONG&code=abc")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("state-mismatch callback = %d, want 400", resp.StatusCode)
	}
}

func TestAuthStart_UnknownProvider404(t *testing.T) {
	prov := &fakeProvider{name: "github", claims: &auth.Claims{}}
	srv, _, _ := newAuthTestServer(t, prov)
	client := noRedirectClient(t)
	resp, err := client.Get(srv.URL + "/auth/google/start")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown provider start = %d, want 404", resp.StatusCode)
	}
}

func TestLogout_ClearsUserCookie(t *testing.T) {
	prov := &fakeProvider{name: "github", claims: &auth.Claims{Provider: "github", Subject: "boxsie"}}
	srv, a, _ := newAuthTestServer(t, prov)
	client := noRedirectClient(t)
	base, _ := url.Parse(srv.URL)

	// Establish a session (tp_sid agent) so CSRF has an identity to check.
	if resp, err := client.Get(srv.URL + "/auth/login"); err != nil {
		t.Fatalf("login: %v", err)
	} else {
		_ = resp.Body.Close()
	}

	// Derive the CSRF token from the agent id in the tp_sid cookie.
	probe := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range client.Jar.Cookies(base) {
		probe.AddCookie(c)
	}
	agentID := a.session.readCookie(probe)
	if agentID == "" {
		t.Fatal("no agent session established")
	}
	token := csrfToken(a.session.secret, agentID)

	form := url.Values{"_csrf": {token}}
	resp, err := client.PostForm(srv.URL+"/auth/logout", form)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout status = %d, want 303", resp.StatusCode)
	}
	// The response must expire the tp_user cookie.
	var cleared bool
	for _, c := range resp.Cookies() {
		if c.Name == userCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not expire the tp_user cookie")
	}
}
