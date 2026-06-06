package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestComments_DetailEmptyState: a fresh ticket renders the "No comments
// yet" empty state in the comments thread.
func TestComments_DetailEmptyState(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "ce", "Comments Empty")
	resp, err := client.Get(srv.URL + "/tickets/" + tid + "?slug=ce")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "No comments yet") {
		t.Errorf("missing empty-state hint\n%s", body)
	}
	if !strings.Contains(body, `name="body"`) {
		t.Errorf("comment form not rendered\n%s", body)
	}
}

// TestComments_Create_Happy: POST a comment, then the new comment appears in
// the rendered thread on next GET.
func TestComments_Create_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "ch", "Comments Happy")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"body":  {"a fresh insight"},
		"_csrf": {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/comments?slug=ch", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	// Now fetch the detail page and check the comment is there.
	resp, err = client.Get(srv.URL + "/tickets/" + tid + "?slug=ch")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if !strings.Contains(body, "a fresh insight") {
		t.Errorf("created comment missing from thread\n%s", body)
	}
	if !strings.Contains(body, `id="comment-`) {
		t.Errorf("comment row missing id anchor\n%s", body)
	}
}

// TestComments_Create_HxRequest_ReturnsRow: POST with HX-Request returns
// just the new comment row partial (no chrome, no form).
func TestComments_Create_HxRequest_ReturnsRow(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "chx", "Comments HX")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"body": {"hx insight"}, "_csrf": {csrf}}
	req, _ := http.NewRequest("POST", srv.URL+"/tickets/"+tid+"/comments", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "comment-form") {
		t.Errorf("HX response must be just the comment row, not chrome or form\n%s", body)
	}
	if !strings.Contains(body, "hx insight") {
		t.Errorf("HX response missing comment body\n%s", body)
	}
}

// TestComments_Create_RejectsEmpty: empty body is rejected by the service.
func TestComments_Create_RejectsEmpty(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "cre", "Comments Empty Body")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"body": {""}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/comments", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("expected non-303, got 303 (empty comment should be rejected)")
	}
}

// TestComments_Create_Optimistic_NoContent: POST with an Idempotency-Key
// header (the optimistic-JS path) returns 204 — the canonical row arrives via
// SSE, not in the POST response — and the comment is persisted.
func TestComments_Create_Optimistic_NoContent(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "cop", "Comments Optimistic")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"body": {"optimistic insight"}, "_csrf": {csrf}}
	req, _ := http.NewRequest("POST", srv.URL+"/tickets/"+tid+"/comments?slug=cop", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", "cid-xyz")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204\n%s", resp.StatusCode, body)
	}
	if body != "" {
		t.Errorf("204 response should have no body, got:\n%s", body)
	}
	// Persisted: it shows up on the next detail GET.
	resp, err = client.Get(srv.URL + "/tickets/" + tid + "?slug=cop")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if !strings.Contains(mustReadAll(t, resp), "optimistic insight") {
		t.Errorf("optimistic comment was not persisted")
	}
}

// TestComments_Create_Optimistic_RejectEmpty: the optimistic path surfaces a
// server rejection as a non-204 error status so the JS can revert.
func TestComments_Create_Optimistic_RejectEmpty(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "coe", "Comments Optimistic Empty")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"body": {""}, "_csrf": {csrf}}
	req, _ := http.NewRequest("POST", srv.URL+"/tickets/"+tid+"/comments", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", "cid-empty")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("empty optimistic comment should error, got %d", resp.StatusCode)
	}
}

// TestComments_List_HxRefresh: GET /tickets/{id}/comments returns the thread
// fragment without page chrome.
func TestComments_List_HxRefresh(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "cl", "Comments List")

	req, _ := http.NewRequest("GET", srv.URL+"/tickets/"+tid+"/comments", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "<html") {
		t.Errorf("HX response must be a partial without chrome\n%s", body)
	}
	if !strings.Contains(body, "comments-list") {
		t.Errorf("comment list container missing\n%s", body)
	}
}

// TestComments_SystemMoveBadge: a system_move comment (created via
// MoveTicket) renders with the system badge.
func TestComments_SystemMoveBadge(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "csm", "System Move")
	csrf := primeCSRF(t, client, srv.URL)
	// Move via the web POST so a system_move comment lands.
	form := url.Values{
		"target_column": {"in_progress"},
		"comment":       {"starting"},
		"_csrf":         {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/move?slug=csm", form)
	if err != nil {
		t.Fatalf("POST move: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	resp, err = client.Get(srv.URL + "/tickets/" + tid + "?slug=csm")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if !strings.Contains(body, "badge-system") {
		t.Errorf("system_move comment must render with system badge\n%s", body)
	}
	if !strings.Contains(body, "todo → in_progress") && !strings.Contains(body, "todo &#39;&#39; in_progress") {
		t.Errorf("system_move comment must render the column transition\n%s", body)
	}
	// Ensure no edit/delete affordance leaks into the rendered thread.
	if strings.Contains(body, "comment-edit") || strings.Contains(body, "comment-delete") {
		t.Errorf("immutable comments must not surface edit/delete affordances\n%s", body)
	}
}

// TestComments_NoEditAffordance: even after a user comment, the rendered
// row carries no edit/delete buttons.
func TestComments_NoEditAffordance(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "cna", "No Edit Affordance")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"body": {"hi"}, "_csrf": {csrf}}
	if _, err := client.PostForm(srv.URL+"/tickets/"+tid+"/comments", form); err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp, err := client.Get(srv.URL + "/tickets/" + tid)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	for _, forbidden := range []string{"comment-edit", "comment-delete", "Edit comment", "Delete comment"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("immutable comments must not expose %q", forbidden)
		}
	}
	_ = context.Background
}
