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

	"tickets_please/internal/config"
)

// ollamaDim is the embedding dimensionality of nomic-embed-text.
const ollamaDim = 768

// ollamaTimeout caps a single Embed call. The first request after a model load
// can take 10+ seconds while Ollama warms up, so we leave generous headroom.
const ollamaTimeout = 60 * time.Second

// Ollama is the embed.Provider backed by a local Ollama server. It uses the
// /api/embeddings HTTP endpoint directly (no SDK).
type Ollama struct {
	url    string
	model  string
	client *http.Client
}

// NewOllama constructs an Ollama provider from cfg. It does not contact the
// server.
func NewOllama(cfg config.Config) *Ollama {
	return &Ollama{
		url:    strings.TrimRight(cfg.OllamaURL, "/"),
		model:  cfg.OllamaModel,
		client: &http.Client{Timeout: ollamaTimeout},
	}
}

// Dim returns 768 (nomic-embed-text).
func (o *Ollama) Dim() int { return ollamaDim }

// Name returns "ollama".
func (o *Ollama) Name() string { return "ollama" }

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Embed POSTs text to ${url}/api/embeddings and returns the resulting vector.
func (o *Ollama) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(ollamaRequest{Model: o.model, Prompt: text})
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
