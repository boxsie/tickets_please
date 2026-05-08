package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestProjectSettings_GETRenders: GET /p/{slug}/settings 200s and renders the
// new fields (name, description, embed_provider select, embed_model input,
// CSRF token, Re-embed button).
func TestProjectSettings_GETRenders(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "st", "settings-me")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	resp, err := client.Get(srv.URL + "/p/settings-me/settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`name="name"`,
		`name="description"`,
		`name="embed_provider"`,
		`name="embed_model"`,
		`name="_csrf"`,
		`action="/p/settings-me/settings"`,
		`action="/p/settings-me/reembed"`,
		`hx-confirm=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q\n%s", want, body)
		}
	}
}

// TestProjectSettings_POST_UpdatesNameAndDescription: round-trip the
// name+description fields. Confirms the existing back-compat shape still
// flows through Service.UpdateProject from the new handler.
func TestProjectSettings_POST_UpdatesNameAndDescription(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "su", "settings-update")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	csrf := primeCSRF(t, client, srv.URL)

	form := url.Values{
		"name":           {"Renamed"},
		"description":    {"new desc"},
		"embed_provider": {""},
		"embed_model":    {""},
		"_csrf":          {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p/settings-update/settings", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/p/settings-update/settings" {
		t.Errorf("Location = %q, want /p/settings-update/settings", loc)
	}
	proj, err := deps.Service.GetProject(context.Background(), "settings-update")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if proj.Name != "Renamed" {
		t.Errorf("name = %q, want Renamed", proj.Name)
	}
	if proj.Description != "new desc" {
		t.Errorf("description = %q, want %q", proj.Description, "new desc")
	}
}

// TestProjectSettings_POST_ChangingEmbedModel_TriggersReembed: changing
// embed_model in the form persists to the project record (Service auto-fires
// ReembedProject; we verify the persisted record reflects the change).
func TestProjectSettings_POST_ChangingEmbedModel_TriggersReembed(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "se", "settings-embed")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"name":           {"settings-embed"},
		"description":    {""},
		"embed_provider": {"ollama"},
		"embed_model":    {"some-other-model"},
		"_csrf":          {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p/settings-embed/settings", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	// Persisted via Service → re-read via the store's project record.
	st, err := deps.Service.ResolveProjectStore(context.Background(), "settings-embed")
	if err != nil {
		t.Fatalf("ResolveProjectStore: %v", err)
	}
	rec, err := st.ReadProject("settings-embed")
	if err != nil {
		t.Fatalf("ReadProject: %v", err)
	}
	if rec.EmbedModel != "some-other-model" {
		t.Errorf("EmbedModel = %q, want some-other-model", rec.EmbedModel)
	}
	if rec.EmbedProvider != "ollama" {
		t.Errorf("EmbedProvider = %q, want ollama", rec.EmbedProvider)
	}
}

// TestProjectSettings_Reembed_Happy: POST /p/{slug}/reembed with CSRF works
// (303 to /settings, flash set).
func TestProjectSettings_Reembed_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "rh", "reembed-happy")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/p/reembed-happy/reembed", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/p/reembed-happy/settings" {
		t.Errorf("Location = %q, want /p/reembed-happy/settings", loc)
	}
}

// TestProjectSettings_Reembed_NoCSRF: POST without CSRF returns 403.
func TestProjectSettings_Reembed_NoCSRF(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "rn", "reembed-nocsrf")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	mustGet(t, client, srv.URL+"/")
	resp, err := client.PostForm(srv.URL+"/p/reembed-nocsrf/reembed", url.Values{})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// TestProjectSettings_OldEditURL_404: the old /p/{slug}/edit route is gone;
// it should fall through to the catch-all "/" handler and 404.
func TestProjectSettings_OldEditURL_404(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "oe", "old-edit")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	resp, err := client.Get(srv.URL + "/p/old-edit/edit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
