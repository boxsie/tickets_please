package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"tickets_please/internal/web/components/md"
)

// TestProjects_IndexEmpty: GET /p with no projects renders the empty hint
// + the create/load CTAs.
func TestProjects_IndexEmpty(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/p")
	if err != nil {
		t.Fatalf("GET /p: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"No projects mounted", "/p/new", "/p/load"} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q\n%s", want, body)
		}
	}
}

// TestProjects_IndexPopulated: register a mount, GET /p shows it.
func TestProjects_IndexPopulated(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "demo-repo", "demo-slug")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	resp, err := client.Get(srv.URL + "/p")
	if err != nil {
		t.Fatalf("GET /p: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "/p/demo-slug") {
		t.Errorf("index missing demo project link\n%s", body)
	}
}

// TestProjects_NewForm: GET /p/new renders the create form with required
// fields + a CSRF hidden field.
func TestProjects_NewForm(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/p/new")
	if err != nil {
		t.Fatalf("GET /p/new: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{
		`name="slug"`, `name="name"`, `name="summary"`, `name="_csrf"`, `action="/p"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("new form missing %q\n%s", want, body)
		}
	}
}

// TestProjects_Create_RejectsMissingCSRF: POST /p without the CSRF token
// must be rejected before any side effects happen.
func TestProjects_Create_RejectsMissingCSRF(t *testing.T) {
	srv, client := freshServer(t)
	// Prime the cookie jar with a session.
	mustGet(t, client, srv.URL+"/")
	form := url.Values{
		"slug":    {"demo"},
		"name":    {"Demo"},
		"summary": {strings.Repeat("x", 250)},
	}
	resp, err := client.PostForm(srv.URL+"/p", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// TestProjects_Create_Happy: POST /p with valid form creates the project,
// emits HX-Trigger: sidebar-refresh, sets a flash cookie, and 303s to detail.
func TestProjects_Create_Happy(t *testing.T) {
	srv, client := freshServer(t)
	csrf := primeCSRF(t, client, srv.URL)

	form := url.Values{
		"slug":        {"demo"},
		"name":        {"Demo"},
		"description": {"d"},
		"summary":     {strings.Repeat("z", 220)},
		"_csrf":       {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p", form)
	if err != nil {
		t.Fatalf("POST /p: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/p/demo" {
		t.Errorf("Location = %q, want /p/demo", loc)
	}
	if trig := resp.Header.Get("HX-Trigger"); trig != "sidebar-refresh" {
		t.Errorf("HX-Trigger = %q, want sidebar-refresh", trig)
	}
	// Flash cookie set.
	hasFlash := false
	for _, c := range resp.Cookies() {
		if c.Name == flashCookieName && c.Value != "" {
			hasFlash = true
		}
	}
	if !hasFlash {
		t.Errorf("no flash cookie emitted")
	}
}

// TestProjects_Create_ShortSummary: POST /p with summary < 200 chars 422s
// and re-renders the form with the typed values.
func TestProjects_Create_ShortSummary(t *testing.T) {
	srv, client := freshServer(t)
	csrf := primeCSRF(t, client, srv.URL)

	form := url.Values{
		"slug":    {"x"},
		"name":    {"X"},
		"summary": {"too short"},
		"_csrf":   {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p", form)
	if err != nil {
		t.Fatalf("POST /p: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "form-error") {
		t.Errorf("missing form-error block\n%s", body)
	}
	// Typed value preserved.
	if !strings.Contains(body, `value="x"`) {
		t.Errorf("typed slug not preserved in re-rendered form")
	}
}

// TestProjects_Load_Happy: POST /p/load against a real on-disk repo mounts
// it, sets sidebar-refresh + flash, redirects to detail.
func TestProjects_Load_Happy(t *testing.T) {
	srv, client := freshServer(t)
	csrf := primeCSRF(t, client, srv.URL)
	repo := seedRepoOnDisk(t, t.TempDir(), "loaded-repo", "loaded-slug")

	form := url.Values{"path": {repo}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/p/load", form)
	if err != nil {
		t.Fatalf("POST /p/load: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/p/loaded-slug" {
		t.Errorf("Location = %q, want /p/loaded-slug", loc)
	}
	if trig := resp.Header.Get("HX-Trigger"); trig != "sidebar-refresh" {
		t.Errorf("HX-Trigger = %q, want sidebar-refresh", trig)
	}
}

// TestProjects_Load_EmptyPath: 422 inline error.
func TestProjects_Load_EmptyPath(t *testing.T) {
	srv, client := freshServer(t)
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"path": {""}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/p/load", form)
	if err != nil {
		t.Fatalf("POST /p/load: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Path is required") {
		t.Errorf("missing 'Path is required' message\n%s", body)
	}
}

// TestProjects_Load_MissingMarker: pointing at a directory without
// .tickets_please/project.yaml surfaces the error inline.
func TestProjects_Load_MissingMarker(t *testing.T) {
	srv, client := freshServer(t)
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"path": {t.TempDir()}, "_csrf": {csrf}} // empty dir
	resp, err := client.PostForm(srv.URL+"/p/load", form)
	if err != nil {
		t.Fatalf("POST /p/load: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("missing marker should not have succeeded")
	}
	if !strings.Contains(body, "form-error") {
		t.Errorf("expected inline form-error\n%s", body)
	}
}

// TestProjects_Detail: register a mount, GET /p/{slug} renders the project.
func TestProjects_Detail(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "demo", "demo-slug")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	resp, err := client.Get(srv.URL + "/p/demo-slug")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"demo-slug", "/p/demo-slug/settings", "/p/demo-slug/summary", "/p/demo-slug/board"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}
}

// TestProjects_Detail_NotFound: 404 for unknown slug.
func TestProjects_Detail_NotFound(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/p/does-not-exist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestProjects_Update: edit form GET + POST update name.
func TestProjects_Update(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "ed", "edit-me")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	csrf := primeCSRF(t, client, srv.URL)

	form := url.Values{
		"name":        {"Renamed"},
		"description": {"new desc"},
		"_csrf":       {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p/edit-me", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	proj, err := deps.Service.GetProject(context.Background(), "edit-me")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if proj.Name != "Renamed" {
		t.Errorf("name = %q, want Renamed", proj.Name)
	}
}

// TestProjects_Delete_RequiresConfirm: POST without confirm=yes is rejected.
func TestProjects_Delete_RequiresConfirm(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "dr", "delete-me")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"_csrf": {csrf}} // no confirm=yes
	resp, err := client.PostForm(srv.URL+"/p/delete-me/delete", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestProjects_Delete_Happy: POST /p/{slug}/delete with confirm=yes removes
// the project and triggers sidebar-refresh.
func TestProjects_Delete_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "dh", "delete-happy")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"confirm": {"yes"}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/p/delete-happy/delete", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if trig := resp.Header.Get("HX-Trigger"); trig != "sidebar-refresh" {
		t.Errorf("HX-Trigger = %q, want sidebar-refresh", trig)
	}
	// Project gone.
	if _, err := deps.Service.GetProject(context.Background(), "delete-happy"); err == nil {
		t.Errorf("GetProject still resolves after delete")
	}
}

// TestProjects_Summary_HxEdit: HX-Request GET ?edit=1 returns the edit
// partial only (no chrome).
func TestProjects_Summary_HxEdit(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "su", "sum-slug")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/p/sum-slug/summary?edit=1", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "id=\"sidebar\"") {
		t.Errorf("partial leaked chrome\n%s", body)
	}
	if !strings.Contains(body, `name="summary"`) || !strings.Contains(body, `name="_csrf"`) {
		t.Errorf("edit partial missing form fields\n%s", body)
	}
}

// TestProjects_Sidebar_Partial: dedicated /_partials/sidebar endpoint
// returns just the <aside> fragment, never the full chrome.
func TestProjects_Sidebar_Partial(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "sb", "side-slug")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	resp, err := client.Get(srv.URL + "/_partials/sidebar")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(body, "<html") {
		t.Errorf("sidebar partial leaked layout\n%s", body)
	}
	if !strings.Contains(body, `id="sidebar"`) {
		t.Errorf("sidebar partial missing #sidebar element\n%s", body)
	}
	if !strings.Contains(body, "/p/side-slug") {
		t.Errorf("sidebar partial missing project link\n%s", body)
	}
}

// TestMarkdown_EscapesRawHTML covers the md.Render safety contract: raw HTML
// tags in source must NOT survive in the output. Goldmark's default config
// (no html.WithUnsafe) replaces each raw HTML tag with
// `<!-- raw HTML omitted -->`. The text between tags survives as inert text
// — not a security issue (it's not inside a <script> tag any more) but worth
// being clear about in the assertion.
func TestMarkdown_EscapesRawHTML(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"script", `<script>alert(1)</script>`},
		{"img-onerror", `<img src=x onerror=alert(1)>`},
		{"iframe", `<iframe src="javascript:alert(1)"></iframe>`},
		{"a-href-js", `<a href="javascript:alert(1)">click</a>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := string(md.Render(tc.src))
			for _, banned := range []string{
				"<script", "<iframe", "onerror=", `href="javascript:`,
			} {
				if strings.Contains(out, banned) {
					t.Errorf("output contains dangerous %q: %q", banned, out)
				}
			}
		})
	}
}

// TestMarkdown_RendersHeadingsAndLinks: sanity check that GFM features work.
func TestMarkdown_RendersHeadingsAndLinks(t *testing.T) {
	out := string(md.Render("# Heading\n\n[link](https://example.com)\n"))
	if !strings.Contains(out, "<h1") || !strings.Contains(out, "Heading") {
		t.Errorf("heading not rendered: %q", out)
	}
	if !strings.Contains(out, `href="https://example.com"`) {
		t.Errorf("link not rendered: %q", out)
	}
}

// --- helpers --------------------------------------------------------------

func mustGet(t *testing.T, client *http.Client, urlStr string) {
	t.Helper()
	resp, err := client.Get(urlStr)
	if err != nil {
		t.Fatalf("GET %s: %v", urlStr, err)
	}
	resp.Body.Close()
}

// primeCSRF: GET / once to mint a session, then GET /p/new and scrape the
// hidden _csrf field out of the response. Returns the token. Same value the
// browser would submit when filling in the form.
func primeCSRF(t *testing.T, client *http.Client, baseURL string) string {
	t.Helper()
	mustGet(t, client, baseURL+"/")
	resp, err := client.Get(baseURL + "/p/new")
	if err != nil {
		t.Fatalf("GET /p/new for CSRF: %v", err)
	}
	body := mustReadAll(t, resp)
	// Crude scrape — the form has exactly one _csrf hidden input.
	const marker = `name="_csrf" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("CSRF marker not found in /p/new response")
	}
	rest := body[i+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("malformed CSRF input in /p/new response")
	}
	return rest[:end]
}

func mustReadAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
