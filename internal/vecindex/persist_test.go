package vecindex

import (
	"path/filepath"
	"testing"
)

// TestSidecarRoundTrip_AllFieldsSurvive writes a Sidecar with non-trivial
// values for every field, reads it back, and asserts each one matches
// byte-for-byte. This is the contract the hydrate/staleness layer relies on.
func TestSidecarRoundTrip_AllFieldsSurvive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "round.embedding.json")

	in := Sidecar{
		Provider: "ollama",
		Model:    "bge-m3",
		Dim:      4,
		Vec:      []float32{0.25, -0.5, 1.5, 0},
	}
	if err := WriteSidecar(path, in); err != nil {
		t.Fatalf("WriteSidecar: %v", err)
	}
	got, err := ReadSidecar(path)
	if err != nil {
		t.Fatalf("ReadSidecar: %v", err)
	}
	if got.Provider != in.Provider {
		t.Errorf("Provider = %q, want %q", got.Provider, in.Provider)
	}
	if got.Model != in.Model {
		t.Errorf("Model = %q, want %q", got.Model, in.Model)
	}
	if got.Dim != in.Dim {
		t.Errorf("Dim = %d, want %d", got.Dim, in.Dim)
	}
	if len(got.Vec) != len(in.Vec) {
		t.Fatalf("len(Vec) = %d, want %d", len(got.Vec), len(in.Vec))
	}
	for i := range in.Vec {
		if got.Vec[i] != in.Vec[i] {
			t.Errorf("Vec[%d] = %v, want %v", i, got.Vec[i], in.Vec[i])
		}
	}
}
