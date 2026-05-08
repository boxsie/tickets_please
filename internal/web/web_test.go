package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	embedpkg "tickets_please/internal/embed"
	"tickets_please/internal/store"
	"tickets_please/internal/svc"
)

// fakeEmbedder is a minimal embed.Provider stand-in so the Service constructor
// doesn't need a live Ollama. It's deterministic but otherwise meaningless —
// these tests don't exercise search.
type fakeEmbedder struct{}

func (fakeEmbedder) Name() string                                         { return "fake" }
func (fakeEmbedder) Dim() int                                             { return 768 }
func (fakeEmbedder) Probe(_ context.Context) error                        { return nil }
func (fakeEmbedder) Embed(_ context.Context, _ string) ([]float32, error) { return make([]float32, 768), nil }

// freshDeps builds a Deps wired to a clean tempdir-backed Service. Every test
// gets its own DataDir + DataRoot so agent yamls don't bleed between tests.
func freshDeps(t *testing.T) Deps {
	t.Helper()
	cfg := config.Config{
		DataDir:                t.TempDir(),
		DataRoot:               t.TempDir(),
		LockTimeoutSeconds:     5,
		AgentSessionTTLMinutes: 60,
		AgentSessionMaxMinutes: 240,
	}
	s, err := svc.NewWithEmbed(cfg, fakeEmbedder{})
	if err != nil {
		t.Fatalf("svc.NewWithEmbed: %v", err)
	}
	t.Cleanup(s.Close)
	// Discard the noisy embed-worker logs; the test cares about HTTP shape.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return Deps{Service: s, Logger: log, Cfg: cfg, Dev: false}
}

// freshServer builds a Deps + httptest.Server with the web Mount applied.
// Returns the server and a *http.Client whose cookie jar is pre-attached so
// callers can chain requests as one "browser session".
func freshServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	mux := http.NewServeMux()
	Mount(mux, freshDeps(t))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		// Don't follow redirects automatically — tests need to see the 303.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return srv, client
}

// freshServerWithDeps is a freshServer variant returning the Deps so a test
// can drive the underlying Service (e.g. RegisterProjectMount) before issuing
// HTTP requests.
func freshServerWithDeps(t *testing.T) (*httptest.Server, *http.Client, Deps) {
	t.Helper()
	deps := freshDeps(t)
	mux := http.NewServeMux()
	Mount(mux, deps)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return srv, client, deps
}

// seedRepoOnDisk writes the minimal .tickets_please/project.yaml that
// RegisterProjectMount needs. Mirrors svc.seedRepo (which lives in a package
// the web tests can't import). Returns the absolute repo path.
func seedRepoOnDisk(t *testing.T, parent, dirName, slug string) string {
	t.Helper()
	repo := filepath.Join(parent, dirName)
	dataDir := filepath.Join(repo, ".tickets_please")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	now := time.Now()
	rec := &store.ProjectRecord{
		ID:        uuid.NewString(),
		Slug:      slug,
		Name:      slug,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.WriteYAMLAtomic(filepath.Join(dataDir, "project.yaml"), rec); err != nil {
		t.Fatalf("write project.yaml: %v", err)
	}
	return repo
}

// fakeChromeProvider is a deterministic ChromeProvider for renderer tests
// that need predictable sidebar/agent/banner data without a Service.
type fakeChromeProvider struct{ chrome Chrome }

func (f fakeChromeProvider) Chrome(_ http.ResponseWriter, _ *http.Request) Chrome {
	return f.chrome
}

// TestRoot_FirstHit_SetsCookie covers the "fresh browser → mint agent" path.
// First GET / must respond 200 with a Set-Cookie tp_sid=...; HttpOnly. Second
// GET with the cookie attached must NOT issue a new Set-Cookie.
func TestRoot_FirstHit_SetsCookie(t *testing.T) {
	srv, client := freshServer(t)

	// First hit.
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("first GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first hit: status = %d, want 200", resp.StatusCode)
	}
	cookies := resp.Cookies()
	var sid *http.Cookie
	for _, c := range cookies {
		if c.Name == cookieName {
			sid = c
			break
		}
	}
	if sid == nil {
		t.Fatalf("first hit: no %s cookie set", cookieName)
	}
	if !sid.HttpOnly {
		t.Errorf("cookie should be HttpOnly")
	}
	if sid.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite = %v, want Lax", sid.SameSite)
	}
	if !strings.Contains(sid.Value, ".") {
		t.Errorf("cookie value missing signature separator: %q", sid.Value)
	}

	// Second hit reuses cookie via the jar.
	resp2, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	resp2.Body.Close()
	for _, c := range resp2.Cookies() {
		if c.Name == cookieName {
			t.Errorf("second hit minted a new cookie: %s = %q", c.Name, c.Value)
		}
	}
}

// TestRoot_TamperedCookie_MintsFresh covers the "cookie HMAC fails → mint
// new" recovery path. The session manager mustn't blow up on a malformed
// cookie; it should fall back to issuing a fresh one.
func TestRoot_TamperedCookie_MintsFresh(t *testing.T) {
	srv, _ := freshServer(t)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "not-a-real-id.invalidsig"})

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			got = c
		}
	}
	if got == nil || got.Value == "not-a-real-id.invalidsig" {
		t.Fatalf("tampered cookie should have been replaced; got = %v", got)
	}
}

// TestStatic_Serves checks that /static/app.css resolves to the embedded
// stylesheet with the right content type.
func TestStatic_Serves(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/static/app.css")
	if err != nil {
		t.Fatalf("GET static: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css*", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 {
		t.Errorf("empty body")
	}
	// Sanity check: the chrome classes we wrote in input.css survived purge.
	if !strings.Contains(string(body), ".sidebar") || !strings.Contains(string(body), ".topbar") {
		t.Errorf("compiled CSS missing chrome classes (regenerate via static/README.md)")
	}
}

// TestStatic_DoesNotLeakSrc confirms the Tailwind sources under static/_src/
// are NOT embedded into the binary (Go's go:embed skips _-prefixed entries).
func TestStatic_DoesNotLeakSrc(t *testing.T) {
	srv, client := freshServer(t)
	for _, p := range []string{"/static/_src/input.css", "/static/_src/tailwind.config.js"} {
		resp, err := client.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s leaked into binary (status 200, want 404)", p)
		}
	}
}

// TestRoot_404OnUnknownPath checks the catch-all "/" handler refuses paths
// it doesn't own (e.g. /favicon.ico). Without this the home page would 200
// for everything not yet registered by future tickets.
func TestRoot_404OnUnknownPath(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/favicon.ico")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestCSRF_RoundTrip verifies the CSRF helper: stable token per agentID,
// different per agent, accepts matching submission, rejects empty/wrong.
func TestCSRF_RoundTrip(t *testing.T) {
	secret := []byte("test-secret")
	a := csrfToken(secret, "agent-A")
	b := csrfToken(secret, "agent-B")
	a2 := csrfToken(secret, "agent-A")
	if a == "" || b == "" {
		t.Fatalf("csrfToken returned empty")
	}
	if a == b {
		t.Errorf("csrfToken should differ across agents")
	}
	if a != a2 {
		t.Errorf("csrfToken should be stable for the same agent")
	}

	// Form with the right token round-trips clean.
	good := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("_csrf="+a))
	good.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := checkCSRF(good, secret, "agent-A"); err != nil {
		t.Errorf("matching token rejected: %v", err)
	}

	// Empty form → reject.
	empty := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(""))
	empty.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := checkCSRF(empty, secret, "agent-A"); err == nil {
		t.Errorf("empty token accepted")
	}

	// Wrong-agent token → reject.
	wrong := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("_csrf="+b))
	wrong.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := checkCSRF(wrong, secret, "agent-A"); err == nil {
		t.Errorf("cross-agent token accepted")
	}
}

// TestRenderer_Page parses and renders the embedded home page through the
// real prod (embed.FS) path. Confirms the layout-wraps-page contract works
// end-to-end with the templates ticket 2 ships — head + nav + sidebar + main.
func TestRenderer_Page(t *testing.T) {
	r := NewRenderer(templatesFS(false), false, nil)
	out, err := r.renderInline("home", PageOpts{Title: "test-title"})
	if err != nil {
		t.Fatalf("renderInline: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"<title>test-title</title>",
		"tickets_please",            // brand link in nav
		"action=\"/search\"",        // search form target
		"id=\"sidebar\"",            // sidebar present
		"No projects mounted",       // sidebar empty state
		"Welcome to tickets_please", // home empty state copy
		"/p/load",                   // sidebar action link
		"/p/new",                    // sidebar action link
		"/static/htmx.min.js",       // htmx loaded
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered output missing %q\n--- got ---\n%s", want, s)
		}
	}
}

// TestRenderer_Partial confirms partials render without dragging the layout.
// Used by htmx swap responses.
func TestRenderer_Partial(t *testing.T) {
	r := NewRenderer(templatesFS(false), false, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Partial(rec, req, "error", errorData{Status: 422, Message: "boom"})
	body := rec.Body.String()
	if strings.Contains(body, "<html") {
		t.Errorf("partial leaked layout: %s", body)
	}
	if strings.Contains(body, "id=\"sidebar\"") {
		t.Errorf("partial leaked sidebar: %s", body)
	}
	if !strings.Contains(body, "Error 422") || !strings.Contains(body, "boom") {
		t.Errorf("partial missing payload: %s", body)
	}
}

// TestRenderer_Sidebar_ActiveItem covers the sidebar's CurrentSlug-driven
// highlight: the matching project gets aria-current="page" + .is-active, the
// others don't. Drives the renderer through a fakeChromeProvider so we don't
// need a Service for this purely-template behaviour.
func TestRenderer_Sidebar_ActiveItem(t *testing.T) {
	provider := fakeChromeProvider{chrome: Chrome{
		Projects: []*domain.Project{
			{Slug: "alpha", Name: "Alpha"},
			{Slug: "beta", Name: "Beta"},
		},
		AgentLabel: "Web UI · abc123",
		URL:        "/p/beta",
	}}
	r := NewRenderer(templatesFS(false), false, provider)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/p/beta", nil)
	r.Page(rec, req, "home", PageOpts{Title: "x", CurrentSlug: "beta"})

	body := rec.Body.String()

	// Both project links present in the picker.
	for _, slug := range []string{"alpha", "beta"} {
		if !strings.Contains(body, "/p/"+slug) {
			t.Errorf("sidebar missing /p/%s link", slug)
		}
	}

	// The "Overview" link in the per-project nav for beta should carry
	// aria-current="page" — that's the URL we said we're on.
	if !strings.Contains(body, `aria-current="page"`) {
		t.Errorf("active item missing aria-current\n%s", body)
	}
	if strings.Count(body, `aria-current="page"`) != 1 {
		t.Errorf("expected exactly one aria-current=\"page\", got %d", strings.Count(body, `aria-current="page"`))
	}

	// Agent label rendered.
	if !strings.Contains(body, "Web UI · abc123") {
		t.Errorf("nav missing agent label\n%s", body)
	}
}

// TestRenderer_LocalhostBanner_Gating ensures the banner appears only when
// the request host is non-loopback. Loopback variants must NOT show it; an
// arbitrary LAN address MUST show it.
func TestRenderer_LocalhostBanner_Gating(t *testing.T) {
	cases := []struct {
		host       string
		wantBanner bool
	}{
		{"localhost:8765", false},
		{"127.0.0.1:8765", false},
		{"[::1]:8765", false},
		{"192.168.1.50:8765", true},
		{"tickets.example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			provider := fakeChromeProvider{chrome: Chrome{
				ShowLocalBanner: !isLoopbackHost(tc.host),
			}}
			r := NewRenderer(templatesFS(false), false, provider)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = tc.host
			r.Page(rec, req, "home", PageOpts{Title: "x"})

			has := strings.Contains(rec.Body.String(), "banner-warn")
			if has != tc.wantBanner {
				t.Errorf("host=%q: banner present=%v, want %v", tc.host, has, tc.wantBanner)
			}
		})
	}
}

// TestIsLoopbackHost is a unit-level guard around the host classifier so a
// regression in the helper shows up with a clear message rather than via the
// banner-rendering test only.
func TestIsLoopbackHost(t *testing.T) {
	loopback := []string{
		"localhost", "localhost:8765", "127.0.0.1", "127.0.0.1:8080",
		"[::1]:1234", "::1", "foo.localhost", "FOO.LOCALHOST",
	}
	notLoopback := []string{
		"192.168.1.1", "10.0.0.5:9000", "tickets.example.com",
		"[2001:db8::1]:80",
	}
	for _, h := range loopback {
		if !isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = false, want true", h)
		}
	}
	for _, h := range notLoopback {
		if isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = true, want false", h)
		}
	}
}

// TestRoot_HxRequest_StripsChrome confirms HX-Request: true causes Page to
// fall through to Partial — no <html>, no sidebar/nav.
func TestRoot_HxRequest_StripsChrome(t *testing.T) {
	srv, client := freshServer(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	for _, banned := range []string{"<html", "<aside", "id=\"sidebar\"", "<header"} {
		if strings.Contains(s, banned) {
			t.Errorf("partial response contained chrome marker %q\n%s", banned, s)
		}
	}
}

// TestHome_RedirectsToFirstProject covers the populated-state branch of
// handleHome: when at least one project is mounted, GET / returns 303 to the
// alphabetically-first slug rather than rendering the empty state. Uses
// RegisterProjectMount because the bare DataDir tempdir starts empty.
func TestHome_RedirectsToFirstProject(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	tmp := t.TempDir()
	repoZ := seedRepoOnDisk(t, tmp, "z-repo", "zeta")
	repoA := seedRepoOnDisk(t, tmp, "a-repo", "alpha")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repoZ); err != nil {
		t.Fatalf("register zeta: %v", err)
	}
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repoA); err != nil {
		t.Fatalf("register alpha: %v", err)
	}

	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/p/alpha" {
		t.Errorf("Location = %q, want /p/alpha (alphabetical first)", loc)
	}
}

// TestLoadProject_GETrenders covers the GET form for /p/load. POST behaviour
// (the actual mount flow) is exercised by the project-handler tests against
// real on-disk repos.
func TestLoadProject_GETrenders(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/p/load")
	if err != nil {
		t.Fatalf("GET /p/load: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{
		`name="path"`, `action="/p/load"`, `name="_csrf"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("load form missing %q\n%s", want, body)
		}
	}
}

// TestFlash_RoundTrip exercises SetFlash + readAndClearFlash via direct
// httptest plumbing. Verifies the cookie is consumed on read so a flash
// doesn't survive across two renders.
func TestFlash_RoundTrip(t *testing.T) {
	// Set a flash on response 1.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	SetFlash(rec, req, "success", "Project loaded.")

	// Take the cookie back into a new request.
	cookies := rec.Result().Cookies()
	var flash *http.Cookie
	for _, c := range cookies {
		if c.Name == flashCookieName {
			flash = c
		}
	}
	if flash == nil {
		t.Fatalf("SetFlash didn't write a %s cookie", flashCookieName)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/y", nil)
	req2.AddCookie(flash)
	got := readAndClearFlash(rec2, req2)
	if got == nil {
		t.Fatalf("readAndClearFlash returned nil for valid flash cookie")
	}
	if got.Kind != "success" || got.Message != "Project loaded." {
		t.Errorf("flash payload = %+v, want {Kind:success Message:Project loaded.}", got)
	}

	// rec2 must clear the cookie (Max-Age<=0).
	cleared := false
	for _, c := range rec2.Result().Cookies() {
		if c.Name == flashCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("readAndClearFlash didn't emit a clearing Set-Cookie")
	}
}

// TestEmbeddedFS_HasExpectedAssets is a paranoia check that go:embed picked
// up the right files. Catches typos in the embed directive at compile time
// is great, but a runtime smoke is cheap and surfaces missing files clearly.
func TestEmbeddedFS_HasExpectedAssets(t *testing.T) {
	want := []string{
		"templates/_layout.tmpl",
		"templates/_nav.tmpl",
		"templates/pages/home.tmpl",
		"templates/pages/projects/load.tmpl",
		"templates/partials/error.tmpl",
		"templates/partials/flash.tmpl",
		"templates/partials/sidebar.tmpl",
		"static/app.css",
		"static/htmx.min.js",
	}
	for _, p := range want {
		var found bool
		// The embed directives are split per FS; check both.
		if _, err := embeddedTemplates.Open(p); err == nil {
			found = true
		}
		if _, err := embeddedStatic.Open(p); err == nil {
			found = true
		}
		if !found {
			t.Errorf("missing embedded asset %q", p)
		}
	}

	// Negative: src dir must NOT be embedded (Go's _-prefix exclusion).
	for _, p := range []string{"static/_src/input.css", "static/_src/tailwind.config.js"} {
		if _, err := embeddedStatic.Open(p); err == nil {
			t.Errorf("%s leaked into embeddedStatic; go:embed should skip _-prefixed entries", p)
		}
	}
}

// Compile-time check that the embed import isn't accidentally unused if the
// test file ever drops its embed reference.
var _ = embedpkg.Provider(fakeEmbedder{})
