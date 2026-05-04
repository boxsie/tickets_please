package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFS_BrowseHome: GET /api/fs without ?path defaults to $HOME and
// returns a JSON listing.
func TestFS_BrowseHome(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/api/fs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	var listing fsListing
	if err := json.Unmarshal([]byte(body), &listing); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, body)
	}
	if listing.Cwd == "" {
		t.Errorf("Cwd empty, want $HOME or /")
	}
}

// TestFS_BrowsePath: GET /api/fs?path=<tempdir> returns the subdirectories.
// The fixture creates two subdirs, one with a marker.
func TestFS_BrowsePath(t *testing.T) {
	srv, client := freshServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "loadable", ".tickets_please"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "loadable", ".tickets_please", "project.yaml"), []byte("slug: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL + "/api/fs?path=" + url.QueryEscape(root))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	var listing fsListing
	if err := json.Unmarshal([]byte(body), &listing); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, body)
	}
	if listing.Cwd != root {
		t.Errorf("Cwd = %q, want %q", listing.Cwd, root)
	}
	if len(listing.Entries) != 2 {
		t.Fatalf("entries = %d, want 2 (loadable + plain)", len(listing.Entries))
	}
	// Sorted alphabetically: loadable < plain.
	if listing.Entries[0].Name != "loadable" || !listing.Entries[0].HasMarker {
		t.Errorf("entries[0] = %+v, want loadable with marker", listing.Entries[0])
	}
	if listing.Entries[1].Name != "plain" || listing.Entries[1].HasMarker {
		t.Errorf("entries[1] = %+v, want plain without marker", listing.Entries[1])
	}
}

// TestFS_HxRequest_ReturnsPartial: HX-Request returns the rendered HTML
// fragment, not JSON.
func TestFS_HxRequest_ReturnsPartial(t *testing.T) {
	srv, client := freshServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/fs?path="+url.QueryEscape(root), nil)
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{`id="picker"`, "fs-breadcrumb", "subdir"} {
		if !strings.Contains(body, want) {
			t.Errorf("partial missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "<aside") {
		t.Errorf("partial leaked chrome\n%s", body)
	}
}

// TestFS_RejectsRelativePath: relative paths surface an inline error.
func TestFS_RejectsRelativePath(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/api/fs?path=relative/dir")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\n%s", resp.StatusCode, body)
	}
	var listing fsListing
	if err := json.Unmarshal([]byte(body), &listing); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, body)
	}
	if !strings.Contains(listing.Error, "absolute") {
		t.Errorf("error = %q, want mention of absolute", listing.Error)
	}
}

// TestFS_NonexistentPath: a path that doesn't exist returns a friendly
// "directory not found" error rather than 500.
func TestFS_NonexistentPath(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/api/fs?path=" + url.QueryEscape("/no/such/path/anywhere/here"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "not found") {
		t.Errorf("error didn't mention 'not found'\n%s", body)
	}
}

// TestFS_FiltersHidden: hidden entries (starting with ".") are filtered
// from the listing — but a .tickets_please subdir still flips the parent's
// HasMarker.
func TestFS_FiltersHidden(t *testing.T) {
	srv, client := freshServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "visible"), 0o755); err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL + "/api/fs?path=" + url.QueryEscape(root))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	var listing fsListing
	json.NewDecoder(resp.Body).Decode(&listing)
	resp.Body.Close()
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "visible" {
		t.Errorf("entries = %v, want only 'visible'", listing.Entries)
	}
}

// TestFS_LoadFormHasPicker: GET /p/load renders the picker partial inline,
// so the user lands on a navigable view rather than a bare text input.
func TestFS_LoadFormHasPicker(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/p/load")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{`id="picker"`, "fs-breadcrumb", `id="load-form"`, "manual-entry"} {
		if !strings.Contains(body, want) {
			t.Errorf("/p/load missing %q", want)
		}
	}
}

// TestBreadcrumbsFor: unit-tests the breadcrumb helper against root and
// nested paths so the picker's parent navigation is correct.
func TestBreadcrumbsFor(t *testing.T) {
	cases := []struct {
		in   string
		want []string // labels in order
	}{
		{"/", []string{"/"}},
		{"/home", []string{"/", "home"}},
		{"/home/dan/code", []string{"/", "home", "dan", "code"}},
	}
	for _, c := range cases {
		got := breadcrumbsFor(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %d crumbs, want %d", c.in, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i].Label != c.want[i] {
				t.Errorf("%s: crumb[%d].Label = %q, want %q", c.in, i, got[i].Label, c.want[i])
			}
		}
	}
}
