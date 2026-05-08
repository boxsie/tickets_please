package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	embedpkg "tickets_please/internal/embed"
	"tickets_please/internal/store"
)

// probeFailingEmbedder is a fake provider whose Probe returns the verbatim
// ollama "model not found" error so the handler-level tests can drive the
// "swap was refused, surface it inline" path.
type probeFailingEmbedder struct {
	probeErr error
}

func (probeFailingEmbedder) Name() string { return "ollama" }
func (probeFailingEmbedder) Dim() int     { return 0 }
func (p probeFailingEmbedder) Probe(_ context.Context) error {
	return p.probeErr
}
func (p probeFailingEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, p.probeErr
}

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

// TestProjectSettings_POST_ProbeFailure_RendersInlineError: the dogfood UX
// fix. User picks an embed_model that isn't pulled in Ollama; the rebuild's
// probe call fails. Handler must:
//   - re-render the Settings form (NOT redirect with success flash),
//   - include the verbatim probe error text in the body,
//   - leave the user's typed values in the form (so they can fix and resubmit),
//   - keep the project.yaml *durably written* (the user's intent is recorded
//     even though the swap was refused).
func TestProjectSettings_POST_ProbeFailure_RendersInlineError(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "pf", "probe-fail")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}

	// Swap the per-mount factory so the next rebuild observes a probe error
	// that mirrors what real ollama returns when the model isn't pulled.
	probeMsg := `model "bge-m3" not found, try pulling it first`
	deps.Service.EmbedNew = func(_ embedpkg.EmbedConfig) (embedpkg.Provider, error) {
		return probeFailingEmbedder{probeErr: errors.New(probeMsg)}, nil
	}

	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"name":           {"probe-fail"},
		"description":    {""},
		"embed_provider": {"ollama"},
		"embed_model":    {"bge-m3"},
		"_csrf":          {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p/probe-fail/settings", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := mustReadAll(t, resp)

	// Must NOT be a 303 redirect with a success flash — the swap was refused.
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("status = 303 (silent fallback path); want inline-error re-render")
	}
	// Verbatim probe error must appear in the rendered HTML so the user
	// reading the page can see exactly what went wrong.
	if !strings.Contains(body, "bge-m3") || !strings.Contains(body, "not found") {
		t.Errorf("re-rendered page missing verbatim probe error.\nbody=%s", body)
	}
	// Form should re-render with the user's typed values intact.
	if !strings.Contains(body, `value="bge-m3"`) {
		t.Errorf("re-rendered form lost typed embed_model value.\nbody=%s", body)
	}

	// project.yaml DID get written — the user's intent is recorded.
	st, err := deps.Service.ResolveProjectStore(context.Background(), "probe-fail")
	if err != nil {
		t.Fatalf("ResolveProjectStore: %v", err)
	}
	rec, err := st.ReadProject("probe-fail")
	if err != nil {
		t.Fatalf("ReadProject: %v", err)
	}
	if rec.EmbedModel != "bge-m3" {
		t.Errorf("project.yaml EmbedModel = %q; want bge-m3 (write should be durable)", rec.EmbedModel)
	}
}

// TestProjectSettings_Reembed_ProbeFailure_FlashesError: POST
// /p/{slug}/reembed with a probe-failing factory must flash the verbatim
// error and redirect back to the settings page (not render the global
// error template, which loses page context).
func TestProjectSettings_Reembed_ProbeFailure_FlashesError(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "rf", "reembed-fail")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}

	// Hand-edit the project.yaml so the next ReembedProject sees a
	// (provider, model) drift and triggers the rebuild path.
	st, err := deps.Service.ResolveProjectStore(context.Background(), "reembed-fail")
	if err != nil {
		t.Fatalf("ResolveProjectStore: %v", err)
	}
	rec, err := st.ReadProject("reembed-fail")
	if err != nil {
		t.Fatal(err)
	}
	rec.EmbedProvider = "ollama"
	rec.EmbedModel = "bge-m3"
	if err := store.WriteYAMLAtomic(filepath.Join(st.Root, "project.yaml"), rec); err != nil {
		t.Fatal(err)
	}
	probeMsg := `model "bge-m3" not found, try pulling it first`
	deps.Service.EmbedNew = func(_ embedpkg.EmbedConfig) (embedpkg.Provider, error) {
		return probeFailingEmbedder{probeErr: errors.New(probeMsg)}, nil
	}

	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/p/reembed-fail/reembed", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (flash + redirect on probe failure)", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/p/reembed-fail/settings" {
		t.Errorf("Location = %q, want /p/reembed-fail/settings", loc)
	}
	// Flash cookie should hold the error message.
	var flashSet bool
	for _, c := range resp.Cookies() {
		if c.Name == flashCookieName && c.Value != "" {
			flashSet = true
		}
	}
	if !flashSet {
		t.Error("expected an error flash cookie after probe-failure reembed")
	}
}

// TestSettings_ReembedAll_PartialFailure_FlashesPerProjectErrors: when
// /settings/reembed-all hits a mount whose probe fails, the flash should
// include both the queued count and the per-project failure message so the
// user knows which slug is broken.
func TestSettings_ReembedAll_PartialFailure_FlashesPerProjectErrors(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	tmp := t.TempDir()
	repoOK := seedRepoOnDisk(t, tmp, "ok", "ok-slug")
	repoBad := seedRepoOnDisk(t, tmp, "bad", "bad-slug")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repoOK); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repoBad); err != nil {
		t.Fatal(err)
	}
	// Drift bad-slug's yaml so its rebuild path runs against the failing
	// factory; ok-slug stays on the default fake which probes fine.
	stBad, err := deps.Service.ResolveProjectStore(context.Background(), "bad-slug")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := stBad.ReadProject("bad-slug")
	if err != nil {
		t.Fatal(err)
	}
	rec.EmbedProvider = "ollama"
	rec.EmbedModel = "bge-m3"
	if err := store.WriteYAMLAtomic(filepath.Join(stBad.Root, "project.yaml"), rec); err != nil {
		t.Fatal(err)
	}
	deps.Service.EmbedNew = func(view embedpkg.EmbedConfig) (embedpkg.Provider, error) {
		if view.Model == "bge-m3" {
			return probeFailingEmbedder{probeErr: errors.New(`model "bge-m3" not found`)}, nil
		}
		return fakeEmbedder{}, nil
	}

	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/settings/reembed-all", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	// Flash should mention bad-slug + the model name so the user can
	// triage which project blocked the swap.
	var flashVal string
	for _, c := range resp.Cookies() {
		if c.Name == flashCookieName {
			flashVal = c.Value
		}
	}
	if flashVal == "" {
		t.Fatal("expected a flash cookie set after partial-failure reembed-all")
	}
	// Flash cookie value is signed/encoded; decode by following the redirect
	// and reading the rendered page for the embedded message.
	resp2, err := client.Get(srv.URL + "/settings")
	if err != nil {
		t.Fatalf("GET /settings: %v", err)
	}
	body := mustReadAll(t, resp2)
	if !strings.Contains(body, "bad-slug") {
		t.Errorf("settings page missing bad-slug in failure flash:\n%s", body)
	}
	if !strings.Contains(body, "bge-m3") {
		t.Errorf("settings page missing model name in failure flash:\n%s", body)
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
