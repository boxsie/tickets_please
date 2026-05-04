package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"tickets_please/internal/domain"
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

	// 10. Search wires up. With a mounted project plus a completed ticket,
	// the learnings index is non-empty; we just assert the response shape
	// is a 200 — the embedder is fakeEmbedder so similarity scores are
	// uninteresting but the dispatcher walks the right service path.
	resp, err = client.Get(srv.URL + "/search?q=smoke&kind=learnings")
	if err != nil {
		t.Fatalf("GET /search: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d, want 200", resp.StatusCode)
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
