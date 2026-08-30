package svc

import (
	"context"
	"testing"

	"tickets_please/internal/config"
)

// dimEmbed is a test-only Provider whose dim is settable and Probe is a
// no-op. Used to confirm the dim flows from the provider through Service.EmbedDim.
type dimEmbed struct{ dim int }

func (d *dimEmbed) Name() string                  { return "fake-dim" }
func (d *dimEmbed) Dim() int                      { return d.dim }
func (d *dimEmbed) Probe(_ context.Context) error { return nil }
func (d *dimEmbed) Embed(_ context.Context, _ string) ([]float32, error) {
	return make([]float32, d.dim), nil
}

func TestNewWithEmbed_DimFlowsFromProvider(t *testing.T) {
	for _, want := range []int{768, 1024, 1536} {
		t.Run("", func(t *testing.T) {
			cfg := config.Config{
				DataDir:                t.TempDir(),
				LockTimeoutSeconds:     5,
				AgentSessionTTLMinutes: 60,
				AgentSessionMaxMinutes: 240,
			}
			s, err := NewWithEmbed(cfg, &dimEmbed{dim: want})
			if err != nil {
				t.Fatalf("NewWithEmbed: %v", err)
			}
			t.Cleanup(s.Close)
			if s.EmbedDim != want {
				t.Errorf("Service.EmbedDim = %d, want %d", s.EmbedDim, want)
			}
		})
	}
}

func TestNewWithEmbed_NilProviderRejected(t *testing.T) {
	cfg := config.Config{
		DataDir:                t.TempDir(),
		LockTimeoutSeconds:     5,
		AgentSessionTTLMinutes: 60,
		AgentSessionMaxMinutes: 240,
	}
	_, err := NewWithEmbed(cfg, nil)
	if err == nil {
		t.Fatal("expected nil-provider error, got nil")
	}
}
