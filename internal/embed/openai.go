package embed

import (
	"context"
	"fmt"

	openai "github.com/sashabaranov/go-openai"

	"tickets_please/internal/config"
)

// openAIModel is the embedding model used. Hardcoded by spec.
const openAIModel = openai.SmallEmbedding3

// OpenAI is the embed.Provider backed by the OpenAI embeddings API.
type OpenAI struct {
	client *openai.Client
	dim    int
}

// NewOpenAI constructs an OpenAI provider from cfg. The factory (New) checks
// for an empty API key; this constructor does not.
func NewOpenAI(cfg config.Config) *OpenAI {
	return &OpenAI{client: openai.NewClient(cfg.OpenAIKey)}
}

// newOpenAIWithBaseURL is a test helper that points the client at an alternate
// base URL (e.g. httptest.Server). Production code should use NewOpenAI / New.
func newOpenAIWithBaseURL(apiKey, baseURL string) *OpenAI {
	c := openai.DefaultConfig(apiKey)
	c.BaseURL = baseURL
	return &OpenAI{client: openai.NewClientWithConfig(c)}
}

// Probe runs a single Embed call and records the resulting vector length.
func (o *OpenAI) Probe(ctx context.Context) error {
	vec, err := o.Embed(ctx, "ping")
	if err != nil {
		return fmt.Errorf("openai: probe: %w", err)
	}
	o.dim = len(vec)
	return nil
}

// Dim returns the probed embedding dimensionality. Panics if Probe hasn't run.
func (o *OpenAI) Dim() int {
	if o.dim == 0 {
		panic("embed.OpenAI: Dim() called before Probe(); Service.New is supposed to probe first")
	}
	return o.dim
}

// Name returns "openai".
func (o *OpenAI) Name() string { return "openai" }

// Embed calls the OpenAI embeddings endpoint and returns the resulting vector.
func (o *OpenAI) Embed(ctx context.Context, text string) ([]float32, error) {
	resp, err := o.client.CreateEmbeddings(ctx, openai.EmbeddingRequest{
		Input: []string{text},
		Model: openAIModel,
	})
	if err != nil {
		return nil, fmt.Errorf("openai: create embedding: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("openai: empty data in embedding response")
	}
	if len(resp.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("openai: empty embedding vector")
	}
	return resp.Data[0].Embedding, nil
}
