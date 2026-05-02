// Package embed defines the embedding Provider interface and the two
// implementations the system ships with: Ollama (default, local) and OpenAI.
//
// Providers are intentionally oblivious to the rest of the system: they take
// text and return a vector. Schema-vs-dim checks live in the worker startup,
// not here.
package embed

import (
	"context"
	"fmt"

	"tickets_please/internal/config"
)

// Provider is the embedding backend abstraction. Implementations must be safe
// for concurrent use.
type Provider interface {
	// Embed returns the embedding vector for text. Implementations should
	// honor ctx for cancellation and deadlines.
	Embed(ctx context.Context, text string) ([]float32, error)
	// Dim is the dimensionality of the vectors this provider returns.
	Dim() int
	// Name is a short identifier for logs ("ollama", "openai").
	Name() string
}

// New constructs a Provider keyed off cfg.EmbedProvider.
//
// Recognized values: "ollama" (default), "openai". Unknown values return an
// error. For OpenAI an empty cfg.OpenAIKey is also a clear error.
func New(cfg config.Config) (Provider, error) {
	switch cfg.EmbedProvider {
	case "ollama":
		return NewOllama(cfg), nil
	case "openai":
		if cfg.OpenAIKey == "" {
			return nil, fmt.Errorf("embed: openai provider selected but openai_api_key is empty (set OPENAI_API_KEY)")
		}
		return NewOpenAI(cfg), nil
	case "":
		return nil, fmt.Errorf("embed: empty embed_provider; expected one of: ollama, openai")
	default:
		return nil, fmt.Errorf("embed: unknown embed_provider %q; expected one of: ollama, openai", cfg.EmbedProvider)
	}
}
