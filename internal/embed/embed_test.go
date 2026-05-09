package embed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// --- Factory ----------------------------------------------------------------

func TestNewFactoryOllama(t *testing.T) {
	cfg := EmbedConfig{
		Provider:  "ollama",
		Model:     "nomic-embed-text",
		OllamaURL: "http://localhost:11434",
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New(ollama) error = %v", err)
	}
	if _, ok := p.(*Ollama); !ok {
		t.Fatalf("New(ollama) returned %T, want *Ollama", p)
	}
	if p.Name() != "ollama" {
		t.Errorf("Name() = %q, want %q", p.Name(), "ollama")
	}
}

func TestNewFactoryOpenAI(t *testing.T) {
	cfg := EmbedConfig{
		Provider:  "openai",
		OpenAIKey: "sk-test-key",
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New(openai) error = %v", err)
	}
	if _, ok := p.(*OpenAI); !ok {
		t.Fatalf("New(openai) returned %T, want *OpenAI", p)
	}
	if p.Name() != "openai" {
		t.Errorf("Name() = %q, want %q", p.Name(), "openai")
	}
}

func TestNewFactoryOpenAIEmptyKey(t *testing.T) {
	cfg := EmbedConfig{Provider: "openai", OpenAIKey: ""}
	_, err := New(cfg)
	if err == nil {
		t.Fatalf("New(openai, empty key) returned nil error; want clear error")
	}
	if !strings.Contains(err.Error(), "openai_api_key") {
		t.Errorf("error %q does not mention openai_api_key", err.Error())
	}
}

func TestNewFactoryUnknown(t *testing.T) {
	for _, v := range []string{"cohere", "bogus", "OPENAI", " "} {
		cfg := EmbedConfig{Provider: v}
		_, err := New(cfg)
		if err == nil {
			t.Errorf("New(%q) returned nil error; want unknown-provider error", v)
		}
	}
}

func TestNewFactoryEmpty(t *testing.T) {
	_, err := New(EmbedConfig{Provider: ""})
	if err == nil {
		t.Fatalf("New(empty) returned nil error; want error")
	}
}

// --- Ollama unit (httptest) -------------------------------------------------

func TestOllamaEmbedRequestShape(t *testing.T) {
	want := make([]float32, 768)
	for i := range want {
		want[i] = float32(i) / 1000.0
	}

	var gotPath, gotMethod, gotContentType string
	var gotBody struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": want})
	}))
	defer srv.Close()

	o := NewOllama(EmbedConfig{Provider: "ollama", OllamaURL: srv.URL, Model: "nomic-embed-text"})
	got, err := o.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/embeddings" {
		t.Errorf("path = %q, want /api/embeddings", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody.Model != "nomic-embed-text" {
		t.Errorf("body.model = %q, want nomic-embed-text", gotBody.Model)
	}
	if gotBody.Prompt != "hello world" {
		t.Errorf("body.prompt = %q, want %q", gotBody.Prompt, "hello world")
	}
	if len(got) != len(want) {
		t.Fatalf("embedding len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("embedding[%d] = %v, want %v", i, got[i], want[i])
			break
		}
	}
}

func TestOllamaProbeRecordsDim(t *testing.T) {
	for _, dim := range []int{768, 1024} {
		want := make([]float32, dim)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": want})
		}))

		o := NewOllama(EmbedConfig{Provider: "ollama", OllamaURL: srv.URL, Model: "any-model"})
		if err := o.Probe(context.Background()); err != nil {
			t.Fatalf("Probe(dim=%d): %v", dim, err)
		}
		if o.Dim() != dim {
			t.Errorf("Dim() = %d, want %d", o.Dim(), dim)
		}
		srv.Close()
	}
}

func TestOllamaDimPanicsBeforeProbe(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Dim() before Probe() did not panic")
		}
	}()
	o := NewOllama(EmbedConfig{Provider: "ollama", OllamaURL: "http://unused", Model: "x"})
	_ = o.Dim()
}

func TestOllamaEmbedTrimsTrailingSlash(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("got path %q, want /api/embeddings (no double slash)", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{1, 2, 3}})
	}))
	defer srv.Close()

	o := NewOllama(EmbedConfig{Provider: "ollama", OllamaURL: srv.URL + "/", Model: "nomic-embed-text"})
	if _, err := o.Embed(context.Background(), "hi"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
}

func TestOllamaEmbedNon2xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	o := NewOllama(EmbedConfig{Provider: "ollama", OllamaURL: srv.URL, Model: "nomic-embed-text"})
	_, err := o.Embed(context.Background(), "hi")
	if err == nil {
		t.Fatalf("Embed: nil error, want non-2xx error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q does not mention status code", err.Error())
	}
}

func TestOllamaProbeAutoPullsMissingModel(t *testing.T) {
	var (
		embedHits int
		pulled    bool
		pullModel string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/embeddings":
			embedHits++
			if !pulled {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"model \"bge-m3\" not found, try pulling it first"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": make([]float32, 1024)})
		case "/api/pull":
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			pullModel = body.Name
			pulled = true
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	o := NewOllama(EmbedConfig{Provider: "ollama", OllamaURL: srv.URL, Model: "bge-m3"})
	if err := o.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !pulled {
		t.Fatal("expected /api/pull to be called")
	}
	if pullModel != "bge-m3" {
		t.Errorf("pull model = %q, want bge-m3", pullModel)
	}
	if embedHits != 2 {
		t.Errorf("embed hits = %d, want 2 (initial 404 + retry)", embedHits)
	}
	if got := o.Dim(); got != 1024 {
		t.Errorf("Dim() = %d, want 1024", got)
	}
}

func TestOllamaProbeUnrelated404DoesNotPull(t *testing.T) {
	var pulled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/pull" {
			pulled = true
			w.WriteHeader(http.StatusOK)
			return
		}
		// 404 with a different message — e.g. wrong endpoint, generic gateway 404 — must NOT trigger a pull.
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	o := NewOllama(EmbedConfig{Provider: "ollama", OllamaURL: srv.URL, Model: "bge-m3"})
	if err := o.Probe(context.Background()); err == nil {
		t.Fatal("Probe: expected error, got nil")
	}
	if pulled {
		t.Error("/api/pull was called for an unrelated 404 — should only auto-pull on 'not found, try pulling it' message")
	}
}

// --- Ollama smoke (real server, gated) --------------------------------------

// TestOllamaSmoke hits a real Ollama and confirms a 768-dim vector.
// Skipped when Ollama isn't reachable at OLLAMA_URL or when the configured
// model isn't pulled locally. To run explicitly:
//
//	go test ./internal/embed -run TestOllamaSmoke -v
func TestOllamaSmoke(t *testing.T) {
	cfg := EmbedConfig{
		Provider:  "ollama",
		Model:     "nomic-embed-text",
		OllamaURL: "http://localhost:11434",
	}

	if !ollamaReachable(cfg.OllamaURL, 500*time.Millisecond) {
		t.Skipf("ollama not reachable at %s; skipping smoke test", cfg.OllamaURL)
	}
	if ok, why := ollamaHasModel(cfg.OllamaURL, cfg.Model, 2*time.Second); !ok {
		t.Skipf("ollama at %s does not have model %q (%s); skipping. Run: ollama pull %s",
			cfg.OllamaURL, cfg.Model, why, cfg.Model)
	}

	o := NewOllama(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	vec, err := o.Embed(ctx, "smoke test: tickets_please T08")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 768 {
		t.Fatalf("len(vec) = %d, want 768", len(vec))
	}
}

// ollamaReachable does a fast TCP dial against the host:port of urlStr to
// gate the smoke test. We dial rather than HTTP-GET so a slow first-load on
// /api/tags doesn't appear as "unreachable".
func ollamaReachable(urlStr string, timeout time.Duration) bool {
	u, err := url.Parse(urlStr)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		// default Ollama port
		host = host + ":11434"
	}
	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ollamaHasModel queries /api/tags and reports whether name (or name:latest) is
// present locally. Returns (false, reason) if the model is missing or the
// query failed.
func ollamaHasModel(baseURL, name string, timeout time.Duration) (bool, string) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/tags", nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return false, "tags status " + resp.Status
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, "tags decode: " + err.Error()
	}
	want1 := name
	want2 := name + ":latest"
	for _, m := range body.Models {
		if m.Name == want1 || m.Name == want2 {
			return true, ""
		}
	}
	return false, "model not pulled"
}

// --- OpenAI unit (httptest) -------------------------------------------------

func TestOpenAIEmbedRequestShape(t *testing.T) {
	want := make([]float32, 1536)
	for i := range want {
		want[i] = float32(i) / 1500.0
	}

	var gotPath, gotMethod, gotAuth string
	var gotBody struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")

		body, _ := io.ReadAll(r.Body)
		// SDK serializes Input via map[string]any so it may come back as []any.
		var raw map[string]any
		_ = json.Unmarshal(body, &raw)
		if m, ok := raw["model"].(string); ok {
			gotBody.Model = m
		}
		if in, ok := raw["input"].([]any); ok {
			for _, v := range in {
				if s, ok := v.(string); ok {
					gotBody.Input = append(gotBody.Input, s)
				}
			}
		}

		resp := map[string]any{
			"object": "list",
			"data": []map[string]any{
				{
					"object":    "embedding",
					"index":     0,
					"embedding": want,
				},
			},
			"model": "text-embedding-3-small",
			"usage": map[string]int{"prompt_tokens": 4, "total_tokens": 4},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// The SDK appends paths like "/embeddings" to the configured BaseURL.
	o := newOpenAIWithBaseURL("sk-test", srv.URL)
	got, err := o.Embed(context.Background(), "hello openai")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/embeddings") {
		t.Errorf("path = %q, want suffix /embeddings", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer sk-test")
	}
	if gotBody.Model != "text-embedding-3-small" {
		t.Errorf("body.model = %q, want text-embedding-3-small", gotBody.Model)
	}
	if len(gotBody.Input) != 1 || gotBody.Input[0] != "hello openai" {
		t.Errorf("body.input = %v, want [hello openai]", gotBody.Input)
	}
	if len(got) != 1536 {
		t.Fatalf("len(vec) = %d, want 1536", len(got))
	}
	for i := 0; i < 5; i++ {
		if got[i] != want[i] {
			t.Errorf("vec[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestOpenAIEmbedHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	o := newOpenAIWithBaseURL("sk-bogus", srv.URL)
	_, err := o.Embed(context.Background(), "hi")
	if err == nil {
		t.Fatalf("Embed: nil error, want error")
	}
	// Sanity: error is wrapped, mentions "openai" prefix from our wrap.
	if !strings.Contains(err.Error(), "openai:") {
		t.Errorf("error %q lacks openai: prefix", err.Error())
	}
	// Make sure it's not a context error.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("unexpected context error: %v", err)
	}
}
