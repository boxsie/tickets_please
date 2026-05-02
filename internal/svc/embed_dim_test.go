package svc

import (
	"context"
	"strings"
	"testing"

	"tickets_please/internal/config"
)

// wrongDimEmbed is a test-only Provider that returns a non-768 dim. Used to
// confirm the dim mismatch error in NewWithEmbed.
type wrongDimEmbed struct{}

func (wrongDimEmbed) Name() string                                      { return "wrong-dim" }
func (wrongDimEmbed) Dim() int                                          { return 1536 }
func (wrongDimEmbed) Embed(_ context.Context, _ string) ([]float32, error) { return nil, nil }

func TestNewWithEmbed_RejectsWrongDim(t *testing.T) {
	cfg := config.Config{
		DataDir:                t.TempDir(),
		LockTimeoutSeconds:     5,
		AgentSessionTTLMinutes: 60,
		AgentSessionMaxMinutes: 240,
	}
	_, err := NewWithEmbed(cfg, wrongDimEmbed{})
	if err == nil {
		t.Fatal("expected dim mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "768") || !strings.Contains(err.Error(), "wrong-dim") {
		t.Errorf("error %q lacks dim/provider context", err)
	}
	if !strings.Contains(err.Error(), "embedding.json") {
		t.Errorf("error %q should hint at deleting *.embedding.json", err)
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
