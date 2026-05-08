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
)

// Provider is the embedding backend abstraction. Implementations must be safe
// for concurrent use.
type Provider interface {
	// Embed returns the embedding vector for text. Implementations should
	// honor ctx for cancellation and deadlines.
	Embed(ctx context.Context, text string) ([]float32, error)
	// Probe contacts the backend once and records the embedding dim so Dim()
	// has something to return. The Service contract is that Probe runs once
	// at startup before anything else asks for Dim().
	Probe(ctx context.Context) error
	// Dim is the dimensionality of the vectors this provider returns.
	// Implementations may panic if called before Probe.
	Dim() int
	// Name is a short identifier for logs ("ollama", "openai").
	Name() string
}

// EmbedConfig is the small projection of config that an embedding provider
// needs. Per-project mounts merge their project.yaml's (provider, model) over
// the server's defaults and pass the resulting view here. Decouples the
// embed package from the full server config shape.
type EmbedConfig struct {
	Provider  string // "ollama" or "openai"
	Model     string // model identifier; ollama uses it verbatim, openai ignores it (model is hardcoded by SDK)
	OllamaURL string // base URL for the local Ollama server
	OpenAIKey string // API key for OpenAI
}

// New constructs a Provider keyed off view.Provider.
//
// Recognized values: "ollama" (default), "openai". Unknown values return an
// error. For OpenAI an empty view.OpenAIKey is also a clear error.
func New(view EmbedConfig) (Provider, error) {
	switch view.Provider {
	case "ollama":
		return NewOllama(view), nil
	case "openai":
		if view.OpenAIKey == "" {
			return nil, fmt.Errorf("embed: openai provider selected but openai_api_key is empty (set OPENAI_API_KEY)")
		}
		return NewOpenAI(view), nil
	case "":
		return nil, fmt.Errorf("embed: empty embed_provider; expected one of: ollama, openai")
	default:
		return nil, fmt.Errorf("embed: unknown embed_provider %q; expected one of: ollama, openai", view.Provider)
	}
}
