package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withConfigPath swaps userConfigPathFn for the duration of a test. Returns a
// path inside a tempdir; callers seed it with a fixture before the POST runs.
func withConfigPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	prev := userConfigPathFn
	userConfigPathFn = func() (string, error) { return path, nil }
	t.Cleanup(func() { userConfigPathFn = prev })
	return path
}

// configFixture is the same hand-curated yaml the config package exercises in
// its SaveYAMLNode tests. Inlined so the web tests don't have to read across
// package testdata boundaries (`go test` runs from each package's own dir).
const configFixture = `# tickets_please configuration
# Top-of-file banner comment.

# Where per-repo project content lives.
data_dir: ./.tickets_please

# Where shared agent state lives (across repos).
data_root: ~/.tickets_please

# --- embedding section ---

# Embedding provider to use: ollama | openai
embed_provider: ollama
ollama_url: http://localhost:11434 # inline comment
ollama_model: nomic-embed-text

# trailing comment
`

// TestSettings_GetRendersValues GETs /settings and confirms the live cfg
// values + the project mount table are present.
func TestSettings_GetRendersValues(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	deps.Service.Cfg.EmbedProvider = "ollama"
	deps.Service.Cfg.OllamaModel = "nomic-embed-text"
	deps.Service.Cfg.OllamaURL = "http://localhost:11434"

	repo := seedRepoOnDisk(t, t.TempDir(), "demo-repo", "demo-slug")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}

	resp, err := client.Get(srv.URL + "/settings")
	if err != nil {
		t.Fatalf("GET /settings: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`name="embed_provider"`,
		`name="embed_model"`,
		`name="ollama_url"`,
		`name="openai_api_key"`,
		`value="nomic-embed-text"`,
		`value="http://localhost:11434"`,
		`/p/demo-slug`,
		`/settings/reembed-all`,
		`/p/demo-slug/reembed`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q\n%s", want, body)
		}
	}
}

// TestSettings_PostRoundTrips_PreservesComments confirms the POST writes back
// to disk via SaveYAMLNode and leaves the surrounding comments intact.
func TestSettings_PostRoundTrips_PreservesComments(t *testing.T) {
	path := withConfigPath(t)
	if err := os.WriteFile(path, []byte(configFixture), 0o644); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	srv, client, _ := freshServerWithDeps(t)
	csrf := primeCSRF(t, client, srv.URL)

	form := url.Values{
		"embed_provider": {"ollama"},
		"embed_model":    {"bge-m3"},
		"ollama_url":     {"http://localhost:11434"},
		"openai_api_key": {""}, // blank: leave key alone
		"_csrf":          {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/settings", form)
	if err != nil {
		t.Fatalf("POST /settings: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "ollama_model: bge-m3") {
		t.Errorf("expected updated model, got:\n%s", out)
	}
	if strings.Contains(out, "nomic-embed-text") {
		t.Errorf("old value still present:\n%s", out)
	}
	for _, want := range []string{
		"# tickets_please configuration",
		"# Where per-repo project content lives.",
		"# --- embedding section ---",
		"# inline comment",
		"# trailing comment",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("comment %q lost in round-trip:\n%s", want, out)
		}
	}
}

// TestSettings_PostBlankOpenAIKey_PreservesExisting confirms the masked-key
// behaviour: blank submit must NOT overwrite a previously-stored key.
func TestSettings_PostBlankOpenAIKey_PreservesExisting(t *testing.T) {
	path := withConfigPath(t)
	seed := configFixture + "openai_api_key: sk-existing-secret\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv, client, deps := freshServerWithDeps(t)
	deps.Service.Cfg.OpenAIKey = "sk-existing-secret"
	csrf := primeCSRF(t, client, srv.URL)

	form := url.Values{
		"embed_provider": {"ollama"},
		"embed_model":    {"bge-m3"},
		"ollama_url":     {"http://localhost:11434"},
		"openai_api_key": {""}, // blank!
		"_csrf":          {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/settings", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "sk-existing-secret") {
		t.Errorf("blank submit wiped existing key:\n%s", out)
	}
	if deps.Service.Cfg.OpenAIKey != "sk-existing-secret" {
		t.Errorf("Cfg.OpenAIKey changed unexpectedly: %q", deps.Service.Cfg.OpenAIKey)
	}
}

// TestSettings_PostNewOpenAIKey_Updates confirms a non-blank submission
// overwrites the stored key.
func TestSettings_PostNewOpenAIKey_Updates(t *testing.T) {
	path := withConfigPath(t)
	if err := os.WriteFile(path, []byte(configFixture), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv, client, deps := freshServerWithDeps(t)
	csrf := primeCSRF(t, client, srv.URL)

	form := url.Values{
		"embed_provider": {"openai"},
		"embed_model":    {"bge-m3"},
		"ollama_url":     {"http://localhost:11434"},
		"openai_api_key": {"sk-fresh-new-key"},
		"_csrf":          {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/settings", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "sk-fresh-new-key") {
		t.Errorf("new key not persisted:\n%s", out)
	}
	if deps.Service.Cfg.OpenAIKey != "sk-fresh-new-key" {
		t.Errorf("Cfg.OpenAIKey not updated: %q", deps.Service.Cfg.OpenAIKey)
	}
	if deps.Service.Cfg.EmbedProvider != "openai" {
		t.Errorf("Cfg.EmbedProvider not updated: %q", deps.Service.Cfg.EmbedProvider)
	}
}

// TestSettings_PostUnknownProvider_RejectedWithInlineError covers the simple
// validation: provider must be ollama|openai, anything else 422s without
// touching the config file.
func TestSettings_PostUnknownProvider_RejectedWithInlineError(t *testing.T) {
	path := withConfigPath(t)
	if err := os.WriteFile(path, []byte(configFixture), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	original, _ := os.ReadFile(path)

	srv, client, _ := freshServerWithDeps(t)
	csrf := primeCSRF(t, client, srv.URL)

	form := url.Values{
		"embed_provider": {"sonnet"},
		"embed_model":    {"x"},
		"ollama_url":     {"x"},
		"_csrf":          {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/settings", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "embed_provider must be") {
		t.Errorf("missing inline error in re-render:\n%s", body)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Errorf("file mutated despite validation failure")
	}
}

// TestSettings_ReembedAll_QueuesAllMounts: register two mounts, POST
// /settings/reembed-all, expect 303 and a flash message reflecting the
// queued count. The Service.ReembedAllProjects path is exercised — the
// fakeEmbedder works synchronously so all mounts are queued.
func TestSettings_ReembedAll_QueuesAllMounts(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	tmp := t.TempDir()
	repoA := seedRepoOnDisk(t, tmp, "ra", "alpha")
	repoB := seedRepoOnDisk(t, tmp, "rb", "beta")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repoA); err != nil {
		t.Fatalf("mount A: %v", err)
	}
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repoB); err != nil {
		t.Fatalf("mount B: %v", err)
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
	if loc := resp.Header.Get("Location"); loc != "/settings" {
		t.Errorf("Location = %q, want /settings", loc)
	}

	// Flash cookie should hold the queued-count message.
	var found bool
	for _, c := range resp.Cookies() {
		if c.Name == flashCookieName && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a flash cookie set after reembed-all")
	}
}

// TestSettings_NavLink: the top-nav Settings link is visible on every page,
// including project pages. Use the home page (no projects mounted) for the
// simplest possible render path.
func TestSettings_NavLink(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, `href="/settings"`) {
		t.Errorf("topnav missing Settings link:\n%s", body)
	}
}
