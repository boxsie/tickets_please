package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ollamaTimeout caps a single Embed call. The first request after a model load
// can take 10+ seconds while Ollama warms up, so we leave generous headroom.
const ollamaTimeout = 60 * time.Second

// Ollama is the embed.Provider backed by a local Ollama server. It uses the
// /api/embeddings HTTP endpoint directly (no SDK).
type Ollama struct {
	url    string
	model  string
	client *http.Client
	dim    int
}

// NewOllama constructs an Ollama provider from view. It does not contact the
// server.
func NewOllama(view EmbedConfig) *Ollama {
	return &Ollama{
		url:    strings.TrimRight(view.OllamaURL, "/"),
		model:  view.Model,
		client: &http.Client{Timeout: ollamaTimeout},
	}
}

// Probe runs a single Embed call against the configured Ollama server and
// records the resulting vector length. The Service guarantees Probe runs once
// before any caller asks for Dim().
func (o *Ollama) Probe(ctx context.Context) error {
	vec, err := o.Embed(ctx, "ping")
	if err != nil {
		return fmt.Errorf("ollama: probe: %w", err)
	}
	o.dim = len(vec)
	return nil
}

// Dim returns the probed embedding dimensionality. Panics if Probe hasn't run
// yet — the Service contract guarantees probe-before-use.
func (o *Ollama) Dim() int {
	if o.dim == 0 {
		panic("embed.Ollama: Dim() called before Probe(); Service.New is supposed to probe first")
	}
	return o.dim
}

// Name returns "ollama".
func (o *Ollama) Name() string { return "ollama" }

type ollamaRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Options map[string]any `json:"options,omitempty"`
}

type ollamaResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Embed POSTs text to ${url}/api/embeddings and returns the resulting vector.
func (o *Ollama) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(ollamaRequest{
		Model:   o.model,
		Prompt:  text,
		Options: map[string]any{"num_ctx": 8192},
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	// Ensure ctx carries a deadline so http.Client.Timeout doesn't silently win.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ollamaTimeout)
		defer cancel()
	}

	endpoint := o.url + "/api/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: post %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		// Read up to a few KB of error body for the message; cap to avoid log floods.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("ollama: %s: status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("ollama: empty embedding in response")
	}
	return out.Embedding, nil
}
