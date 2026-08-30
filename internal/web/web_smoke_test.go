package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
	"tickets_please/internal/vecindex"
)

// TestSmoke_EndToEnd runs the full happy-path flow against the wired-up
// Mount: create a project, create a ticket, move it with a comment, try to
// move-to-done (rejected), complete it (succeeds), and verify the ticket is
// frozen. One linear test rather than a per-step suite so the wall clock
// stays low and the orchestration is obvious.
//
// Mirrors the manual click-through called out in ticket 8's verification but
// in CI form. Total runtime should be well under a second.
func TestSmoke_EndToEnd(t *testing.T) {
	srv, client := freshServer(t)

	// 1. Root sets the session cookie. Tested in detail in web_test.go;
	// here we just need the cookie attached to the jar.
	mustGet(t, client, srv.URL+"/")

	// 2. Static asset serves with the right content-type.
	resp, err := client.Get(srv.URL + "/static/app.css")
	if err != nil {
		t.Fatalf("GET /static/app.css: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/static/app.css status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("/static/app.css Content-Type = %q, want text/css", ct)
	}

	// 3. /healthz survives mounting (regression check that web routes don't
	// shadow the existing health endpoint).
	resp, err = client.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	// /healthz isn't mounted in freshServer (which only attaches web.Mount),
	// so we expect 404 here. The point of this assertion is that the web
	// router doesn't return 200 for a path it doesn't own — i.e. the "/"
	// catch-all matches but its handler doesn't claim healthz.
	if resp.StatusCode == http.StatusOK {
		t.Errorf("web router shouldn't claim /healthz; got 200")
	}

	// 4. Create project via the form.
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"slug":        {"smoke"},
		"name":        {"Smoke Test"},
		"description": {"e2e smoke"},
		"summary":     {strings.Repeat("z", 220)},
		"_csrf":       {csrf},
	}
	resp, err = client.PostForm(srv.URL+"/p", form)
	if err != nil {
		t.Fatalf("POST /p: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /p status = %d, want 303", resp.StatusCode)
	}

	// 5. Create ticket via the form.
	form = url.Values{
		"title": {"smoke-ticket"},
		"body":  {"end-to-end smoke"},
		"wave":  {"0"},
		"_csrf": {csrf},
	}
	resp, err = client.PostForm(srv.URL+"/p/smoke/tickets", form)
	if err != nil {
		t.Fatalf("POST /p/smoke/tickets: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /p/smoke/tickets status = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	tid := strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/tickets/")
	if tid == "" {
		t.Fatalf("create ticket Location lacked ticket id: %q", loc)
	}

	// 6. Move with a comment — the hard rule "comment required" exercised
	// implicitly (empty case covered by handlers_tickets_test.go).
	form = url.Values{
		"target_column": {"in_progress"},
		"comment":       {"smoke move"},
		"_csrf":         {csrf},
	}
	resp, err = client.PostForm(srv.URL+"/tickets/"+tid+"/move?slug=smoke", form)
	if err != nil {
		t.Fatalf("POST move: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST move status = %d, want 303", resp.StatusCode)
	}

	// 7. Move-to-done is rejected. The handler short-circuits with 422
	// before reaching Service so the inline error can point at Complete.
	form = url.Values{
		"target_column": {"done"},
		"comment":       {"trying to skip complete"},
		"_csrf":         {csrf},
	}
	resp, err = client.PostForm(srv.URL+"/tickets/"+tid+"/move", form)
	if err != nil {
		t.Fatalf("POST move-to-done: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("move-to-done status = %d, want 422", resp.StatusCode)
	}

	// 8. Complete with all three required fields lands the ticket in done.
	form = url.Values{
		"testing_evidence": {"manual smoke flow ran clean"},
		"work_summary":     {"the e2e smoke covers the happy path"},
		"learnings":        {"the wiring works end-to-end"},
		"_csrf":            {csrf},
	}
	resp, err = client.PostForm(srv.URL+"/tickets/"+tid+"/complete?slug=smoke", form)
	if err != nil {
		t.Fatalf("POST complete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("complete status = %d, want 303", resp.StatusCode)
	}

	// 9. Frozen check: GET /tickets/{id}/edit on a done ticket returns 422
	// (the handler refuses before redirecting to the form).
	resp, err = client.Get(srv.URL + "/tickets/" + tid + "/edit")
	if err != nil {
		t.Fatalf("GET /edit on done: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("edit-on-done status = %d, want 422", resp.StatusCode)
	}

	// 10. Cross-project /search has been removed (W4-T1); the per-project
	// replacement lands in W4-T2. Assert the route 404s so the smoke test
	// pins the regression cleanly.
	resp, err = client.Get(srv.URL + "/search?q=smoke&kind=learnings")
	if err != nil {
		t.Fatalf("GET /search: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("search status = %d, want 404", resp.StatusCode)
	}
}

// TestSmoke_AuditTrail confirms a comment posted from the web UI lands in
// the audit trail with a Web UI agent ref (not "unknown") — the audit trail
// is the load-bearing identity surface so this is worth a dedicated check.
func TestSmoke_AuditTrail(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "audit", "Audit Trail")
	csrf := primeCSRF(t, client, srv.URL)

	form := url.Values{"body": {"a comment from the web ui"}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/comments?slug=audit", form)
	if err != nil {
		t.Fatalf("POST comment: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST comment status = %d, want 303", resp.StatusCode)
	}

	// Read back via Service. The comment's Author should carry a non-empty
	// Name — the session middleware mints "Web UI · <suffix>" agents.
	comments, err := deps.Service.ListComments(context.Background(), tid)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	var found bool
	for _, c := range comments {
		if c.Kind != domain.CommentKindUser {
			continue
		}
		if c.Author == nil {
			t.Errorf("user comment has nil Author")
			continue
		}
		if c.Author.Name == "" {
			t.Errorf("user comment has empty Author.Name — audit trail would render 'unknown'")
		}
		if !strings.Contains(strings.ToLower(c.Author.Name), "web") {
			t.Logf("note: comment author name %q does not match the 'Web UI' convention; verify session.go", c.Author.Name)
		}
		found = true
	}
	if !found {
		t.Fatalf("user comment missing from thread")
	}
}

// upsertTicketIntoMount embeds `text` via the mount's provider and Upserts
// it as a KindTicketBody entry into the mount's TicketsIdx. Mirrors what the
// embed worker would do but synchronous so the test doesn't have to race the
// worker.
func upsertTicketIntoMount(t *testing.T, deps Deps, slug, ticketID, text string) {
	t.Helper()
	var mount *svc.ProjectMount
	_ = deps.Service.WalkProjectMounts(func(s string, m *svc.ProjectMount) error {
		if s == slug {
			mount = m
		}
		return nil
	})
	if mount == nil {
		t.Fatalf("mount %q not registered", slug)
	}
	vec, err := mount.Embed.Embed(context.Background(), text)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	mount.TicketsIdx.Upsert(vecindex.Entry{
		ID:    ticketID,
		Kind:  vecindex.KindTicketBody,
		Owner: slug,
		Vec:   vec,
	})
}

// TestSmoke_ProjectSearch_HappyPath registers a project, creates a ticket,
// upserts it into the resident TicketsIdx with the same vec the search will
// produce, and confirms GET /p/{slug}/search?q=...&kind=tickets returns 200
// with the ticket title in the body.
func TestSmoke_ProjectSearch_HappyPath(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, tid := seedProjectAndTicket(t, deps, "psh", "Search Hit")
	// Synthesise an index entry for the ticket using a known query string —
	// the test issues that exact string as q so the deterministic fakeEmbedder
	// produces an identical vector and cosine == 1.0.
	upsertTicketIntoMount(t, deps, slug, tid, "embed needle")

	resp, err := client.Get(srv.URL + "/p/" + slug + "/search?q=embed+needle&kind=tickets")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	s := string(body)
	if !strings.Contains(s, "Search Hit") {
		t.Errorf("ticket title missing from results\n%s", s)
	}
	if !strings.Contains(s, "/tickets/"+tid) {
		t.Errorf("hit link missing\n%s", s)
	}
	// Each ticket hit carries the author · time attribution chip.
	if !strings.Contains(s, "ticket-attribution") {
		t.Errorf("attribution chip missing from ticket search hit\n%s", s)
	}
	// Topbar search form should be present (CurrentSlug wired through).
	if !strings.Contains(s, `action="/p/`+slug+`/search"`) {
		t.Errorf("topbar search form missing on project page\n%s", s)
	}
	// Topbar search box should carry its inline magnifying-glass icon and
	// the polished `.search` chrome class (not just the bare wrapper).
	if !strings.Contains(s, `class="search-icon"`) {
		t.Errorf("topbar search icon wrapper missing on project page\n%s", s)
	}
	if !strings.Contains(s, "<svg") {
		t.Errorf("topbar search SVG missing on project page\n%s", s)
	}
}

// TestSmoke_ProjectSearch_TwoProject verifies project-A search returns no
// hits owned by project-B's TicketsIdx — the per-mount routing is what keeps
// the two indexes from contaminating one another.
func TestSmoke_ProjectSearch_TwoProject(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	tmp := t.TempDir()
	repoA := seedRepoOnDisk(t, tmp, "repo-a", "proj-a")
	repoB := seedRepoOnDisk(t, tmp, "repo-b", "proj-b")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repoA); err != nil {
		t.Fatalf("mount A: %v", err)
	}
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repoB); err != nil {
		t.Fatalf("mount B: %v", err)
	}
	// CreateTicket requires an authenticated agent.
	id, _, err := deps.Service.RegisterAgent(context.Background(), "two-proj-fixture", "two-proj",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(context.Background(), id)
	tA, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
		ProjectIDOrSlug: "proj-a", Title: "Alpha-Only Ticket", Body: "alpha body",
	})
	if err != nil {
		t.Fatalf("create ticket A: %v", err)
	}
	tB, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
		ProjectIDOrSlug: "proj-b", Title: "Beta-Only Ticket", Body: "beta body",
	})
	if err != nil {
		t.Fatalf("create ticket B: %v", err)
	}
	tidA, tidB := tA.ID, tB.ID
	upsertTicketIntoMount(t, deps, "proj-a", tidA, "alpha needle text")
	upsertTicketIntoMount(t, deps, "proj-b", tidB, "beta needle text")

	// Search proj-a for beta's text — no hit (cosine differs, but more
	// importantly we shouldn't see beta's id leak through).
	resp, err := client.Get(srv.URL + "/p/proj-a/search?q=beta+needle+text&kind=tickets")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	s := string(body)
	if strings.Contains(s, tidB) {
		t.Errorf("proj-a search leaked proj-b ticket id %s\n%s", tidB, s)
	}
	if strings.Contains(s, "Beta-Only Ticket") {
		t.Errorf("proj-a search leaked proj-b ticket title\n%s", s)
	}
}

// TestSmoke_ProjectSearch_HxRequest_Fragment confirms HX-Request returns just
// the results partial — no <html>, no sidebar.
func TestSmoke_ProjectSearch_HxRequest_Fragment(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, tid := seedProjectAndTicket(t, deps, "hxs", "HX Search")
	upsertTicketIntoMount(t, deps, slug, tid, "needle hx")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/p/"+slug+"/search?q=needle+hx&kind=tickets", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	s := string(body)
	for _, banned := range []string{"<html", "<aside", "id=\"sidebar\"", "<header"} {
		if strings.Contains(s, banned) {
			t.Errorf("partial leaked chrome marker %q\n%s", banned, s)
		}
	}
	if !strings.Contains(s, "HX Search") {
		t.Errorf("results fragment missing hit\n%s", s)
	}
}

// TestSmoke_ProjectSearch_EmptyQuery: an empty q renders "Type to search" and
// does not dial out to the embedder. Implicit guard: if it tried to embed an
// empty string, the response would still be 200 here, but the test pinning
// the hint copy makes the no-op visible.
func TestSmoke_ProjectSearch_EmptyQuery(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _ := seedProjectAndTicket(t, deps, "emp", "Empty Q")

	resp, err := client.Get(srv.URL + "/p/" + slug + "/search")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Type to search") {
		t.Errorf("empty-q search missing hint\n%s", body)
	}
}
