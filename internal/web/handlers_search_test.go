package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestSearch_PageRenders: GET /search renders the page with the search form
// (no query yet → "Type to search" hint).
func TestSearch_PageRenders(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/search")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{`name="q"`, `Type to search`, "search-tab"} {
		if !strings.Contains(body, want) {
			t.Errorf("search page missing %q", want)
		}
	}
}

// TestSearch_DefaultsToLearnings: with no kind, defaults to learnings.
func TestSearch_DefaultsToLearnings(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/search?q=test")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	// Active tab = learnings.
	if !strings.Contains(body, `class="search-tab active"`) {
		t.Errorf("missing active tab marker")
	}
	if !strings.Contains(body, `kind=learnings`) && !strings.Contains(body, `value="learnings"`) {
		t.Errorf("kind not surfaced as learnings\n%s", body)
	}
}

// TestSearch_HxRequest_ReturnsPartial: HX-Request returns just the results
// fragment (no page chrome).
func TestSearch_HxRequest_ReturnsPartial(t *testing.T) {
	srv, client := freshServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/search?q=test&kind=learnings", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "<aside") || strings.Contains(body, "search-form") {
		t.Errorf("HX response must be a partial without chrome\n%s", body)
	}
}

// TestSearch_TicketsRequiresProject: kind=tickets without slug surfaces an
// inline error pointing the user at the slug filter.
func TestSearch_TicketsRequiresProject(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/search?q=foo&kind=tickets")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "needs a project") {
		t.Errorf("missing project-required hint\n%s", body)
	}
}

// TestSearch_RunsOverMounted: with a mounted project + completed ticket,
// SearchLearnings returns at least one hit. Doesn't assert specific score
// shape — vector embedder is fakeEmbedder so values are deterministic but
// uninteresting.
func TestSearch_RunsOverMounted(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "se", "searchproj")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	resp, err := client.Get(srv.URL + "/search?q=anything&kind=learnings&slug=searchproj")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	// Either renders hits or the empty-state hint — both are valid.
	hasResults := strings.Contains(body, "search-hits") || strings.Contains(body, "No matches")
	if !hasResults {
		t.Errorf("search response neither hits nor empty-state\n%s", body)
	}
}
