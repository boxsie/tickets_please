package svc

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/embed"
	"tickets_please/internal/vecindex"
)

// gateEmbed is an embed.Provider whose model starts "unpulled": Probe fails
// with a model-missing error and Embed errors, until EnsureModel is allowed to
// complete. EnsureModel blocks on `release` so the test can prove the
// boot/attach path does NOT wait on the pull. After release it reports a
// 1024-dim model (distinct from the 768-dim server-default fake) so the swap +
// re-embed is observable.
type gateEmbed struct {
	mu          sync.Mutex
	pulled      bool
	pullStarted chan struct{}
	release     chan struct{}
}

func newGateEmbed() *gateEmbed {
	return &gateEmbed{pullStarted: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateEmbed) Name() string { return "ollama" }

func (g *gateEmbed) Dim() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.pulled {
		return 0
	}
	return 1024
}

func (g *gateEmbed) Probe(_ context.Context) error {
	g.mu.Lock()
	pulled := g.pulled
	g.mu.Unlock()
	if !pulled {
		// Must satisfy embed.IsModelMissing: contains "status 404",
		// "not found", and "try pulling it".
		return fmt.Errorf("gate probe: status 404: model not found, try pulling it first")
	}
	return nil
}

func (g *gateEmbed) Embed(_ context.Context, _ string) ([]float32, error) {
	g.mu.Lock()
	pulled := g.pulled
	g.mu.Unlock()
	if !pulled {
		return nil, fmt.Errorf("gate embed: status 404: model not found, try pulling it first")
	}
	return make([]float32, 1024), nil
}

func (g *gateEmbed) EnsureModel(ctx context.Context) error {
	close(g.pullStarted)
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	g.mu.Lock()
	g.pulled = true
	g.mu.Unlock()
	return nil
}

// mountModel reads a mount's stamped (model, dim) under the service lock so the
// race detector doesn't flag the concurrent swap.
func mountModel(s *Service, slug string) (string, int, bool) {
	s.mountsMu.Lock()
	defer s.mountsMu.Unlock()
	m := s.projectMounts[slug]
	if m == nil {
		return "", 0, false
	}
	return m.EmbedModel, m.EmbedDim, true
}

// TestMountFallback_DoesNotBlockOnPull_ThenSwaps is the acceptance test for
// ticket 3a138760: mounting a project whose declared embed model isn't pulled
// must NOT block on the pull. The mount comes up immediately on the server-
// default fallback; the pull runs in the background; when it lands the mount
// swaps to the real model and re-embeds — no restart. It also covers the
// de1a552e truth-stamp: during the fallback window the mount is stamped with
// the model it ACTUALLY embeds with (the server default), not the requested
// one.
func TestMountFallback_DoesNotBlockOnPull_ThenSwaps(t *testing.T) {
	cfg := config.Config{MaxLoadedProjects: 4, OllamaModel: "server-default-model"}
	s := freshServiceNoDataDir(t, cfg)
	t.Cleanup(s.Close)

	gate := newGateEmbed()
	s.EmbedNew = func(_ embed.EmbedConfig) (embed.Provider, error) { return gate, nil }

	repo := seedRepoWithProvider(t, t.TempDir(), "repo", "proj", "ollama", "bge-m3")

	// Mount must return promptly even though the gate's pull will block.
	done := make(chan error, 1)
	go func() {
		_, err := s.RegisterProjectMount(context.Background(), repo)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RegisterProjectMount: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RegisterProjectMount blocked — it must not wait on the model pull")
	}

	// The background acquisition goroutine should have reached the (blocked)
	// pull. While it's blocked, the mount is on the truthful fallback.
	select {
	case <-gate.pullStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("background EnsureModel never started")
	}

	model, dim, ok := mountModel(s, "proj")
	if !ok {
		t.Fatal("proj not mounted")
	}
	if model != "server-default-model" {
		t.Errorf("fallback EmbedModel = %q; want the server-default model (truth, not requested 'bge-m3')", model)
	}
	if dim != 768 {
		t.Errorf("fallback EmbedDim = %d; want 768 (server default)", dim)
	}

	// Let the pull complete; the mount must swap to the real model + dim.
	close(gate.release)

	deadline := time.Now().Add(3 * time.Second)
	for {
		model, dim, _ = mountModel(s, "proj")
		if model == "bge-m3" && dim == 1024 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mount never swapped to real model: model=%q dim=%d", model, dim)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStaleSidecar_DimMismatch is the focused de1a552e regression for the dim
// safety net: a sidecar whose provider+model match the mount but whose vector
// length differs from the mount's probed dim is stale and must be rebuilt.
func TestStaleSidecar_DimMismatch(t *testing.T) {
	mount := &ProjectMount{
		Embed:      &fakeEmbed{}, // Name() == "fake", server-default-ish
		EmbedModel: "m",
		EmbedDim:   1024,
	}

	cases := []struct {
		name string
		sc   vecindex.Sidecar
		want bool
	}{
		{"provider+model+dim all match", vecindex.Sidecar{Provider: "fake", Model: "m", Dim: 1024, Vec: make([]float32, 1024)}, false},
		{"dim mismatch (768 vs 1024)", vecindex.Sidecar{Provider: "fake", Model: "m", Dim: 768, Vec: make([]float32, 768)}, true},
		{"model mismatch", vecindex.Sidecar{Provider: "fake", Model: "other", Dim: 1024, Vec: make([]float32, 1024)}, true},
		{"provider mismatch", vecindex.Sidecar{Provider: "ollama", Model: "m", Dim: 1024, Vec: make([]float32, 1024)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := staleSidecar(tc.sc, mount); got != tc.want {
				t.Errorf("staleSidecar = %v; want %v", got, tc.want)
			}
		})
	}

	// A mount that never probed (EmbedDim 0) must not false-positive on dim.
	unprobed := &ProjectMount{Embed: &fakeEmbed{}, EmbedModel: "m", EmbedDim: 0}
	if staleSidecar(vecindex.Sidecar{Provider: "fake", Model: "m", Dim: 768, Vec: make([]float32, 768)}, unprobed) {
		t.Error("unprobed mount (EmbedDim 0) should not flag a dim mismatch")
	}
}
