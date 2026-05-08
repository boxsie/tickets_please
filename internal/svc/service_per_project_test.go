package svc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/config"
	"tickets_please/internal/embed"
	"tickets_please/internal/store"
	"tickets_please/internal/vecindex"
)

// fakeProvider is a deterministic embed.Provider whose dim is parameterised
// at construction. Used by the per-project test below to mount two projects
// with different probed dims and confirm independence.
type fakeProvider struct {
	name string
	dim  int
}

func (f *fakeProvider) Name() string                  { return f.name }
func (f *fakeProvider) Dim() int                      { return f.dim }
func (f *fakeProvider) Probe(_ context.Context) error { return nil }

func (f *fakeProvider) Embed(_ context.Context, text string) ([]float32, error) {
	out := make([]float32, f.dim)
	seed := sha256.Sum256([]byte(f.name + "|" + text))
	for i := 0; i < f.dim/8; i++ {
		var nonce [4]byte
		binary.BigEndian.PutUint32(nonce[:], uint32(i))
		h := sha256.New()
		h.Write(seed[:])
		h.Write(nonce[:])
		block := h.Sum(nil)
		for j := 0; j < 8; j++ {
			u := binary.BigEndian.Uint32(block[j*4 : j*4+4])
			out[i*8+j] = float32(int32(u)) / float32(math.MaxInt32)
		}
	}
	// Tail floats (when dim isn't a multiple of 8) — leave zero.
	var sum float64
	for _, v := range out {
		sum += float64(v) * float64(v)
	}
	if sum > 0 {
		inv := 1.0 / math.Sqrt(sum)
		for i, v := range out {
			out[i] = float32(float64(v) * inv)
		}
	}
	return out, nil
}

// seedRepoWithProvider writes a project.yaml under <parent>/<dir>/.tickets_please
// stamping the given (embed_provider, embed_model) pair. Returns the absolute
// repo path.
func seedRepoWithProvider(t *testing.T, parent, dirName, slug, embedProvider, embedModel string) string {
	t.Helper()
	repo := filepath.Join(parent, dirName)
	dataDir := filepath.Join(repo, ".tickets_please")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	now := time.Now()
	rec := &store.ProjectRecord{
		ID:            uuid.NewString(),
		Slug:          slug,
		Name:          slug,
		EmbedProvider: embedProvider,
		EmbedModel:    embedModel,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := store.WriteYAMLAtomic(filepath.Join(dataDir, "project.yaml"), rec); err != nil {
		t.Fatalf("write project.yaml: %v", err)
	}
	return repo
}

// TestPerProjectEmbedAndIndexes is the load-bearing W2-T1 verification:
// mount two projects with different probed dims (768 + 1024), confirm each
// mount carries its own dim and own indexes, and that search routed through
// each mount returns only that mount's hits.
func TestPerProjectEmbedAndIndexes(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 4})
	tmp := t.TempDir()

	// Inject a factory that picks the fake by (provider, model). The default
	// freshServiceNoDataDir wires EmbedNew to "always return the injected
	// fake" — override here so each project gets its own dim.
	provFor := func(view embed.EmbedConfig) (embed.Provider, error) {
		switch view.Model {
		case "nomic-embed-text":
			return &fakeProvider{name: "ollama-fake-768", dim: 768}, nil
		case "bge-m3":
			return &fakeProvider{name: "ollama-fake-1024", dim: 1024}, nil
		}
		return &fakeProvider{name: "ollama-fake-768", dim: 768}, nil
	}
	s.EmbedNew = provFor

	repoA := seedRepoWithProvider(t, tmp, "repoAlpha", "alpha", "ollama", "nomic-embed-text")
	repoB := seedRepoWithProvider(t, tmp, "repoBeta", "beta", "ollama", "bge-m3")

	if _, err := s.RegisterProjectMount(context.Background(), repoA); err != nil {
		t.Fatalf("mount alpha: %v", err)
	}
	if _, err := s.RegisterProjectMount(context.Background(), repoB); err != nil {
		t.Fatalf("mount beta: %v", err)
	}

	mountA := s.projectMounts["alpha"]
	mountB := s.projectMounts["beta"]

	// Per-mount dims differ.
	if mountA.EmbedDim != 768 {
		t.Errorf("alpha EmbedDim = %d; want 768", mountA.EmbedDim)
	}
	if mountB.EmbedDim != 1024 {
		t.Errorf("beta EmbedDim = %d; want 1024", mountB.EmbedDim)
	}

	// Per-mount indexes are distinct *vecindex.Index pointers.
	if mountA.SummaryIdx == mountB.SummaryIdx {
		t.Error("alpha and beta share SummaryIdx; want distinct")
	}
	if mountA.TicketsIdx == mountB.TicketsIdx {
		t.Error("alpha and beta share TicketsIdx; want distinct")
	}
	if mountA.LearningsIdx == mountB.LearningsIdx {
		t.Error("alpha and beta share LearningsIdx; want distinct")
	}
	if mountA.CommentsIdx == mountB.CommentsIdx {
		t.Error("alpha and beta share CommentsIdx; want distinct")
	}

	// Per-mount providers are distinct, and each carries its own probed dim.
	if mountA.Embed.Dim() != 768 {
		t.Errorf("alpha provider Dim = %d; want 768", mountA.Embed.Dim())
	}
	if mountB.Embed.Dim() != 1024 {
		t.Errorf("beta provider Dim = %d; want 1024", mountB.Embed.Dim())
	}
	if mountA.Embed == mountB.Embed {
		t.Error("alpha and beta share Embed provider; want distinct instances")
	}

	// Manually upsert one ticket-body entry per mount, then confirm
	// SearchTickets routed through each slug returns only that mount's hit.
	// Vec dim must match the mount's EmbedDim or Search filters it out.
	alphaVec, _ := mountA.Embed.Embed(context.Background(), "alpha-only ticket text")
	betaVec, _ := mountB.Embed.Embed(context.Background(), "beta-only ticket text")
	alphaTicketID := uuid.NewString()
	betaTicketID := uuid.NewString()
	mountA.TicketsIdx.Upsert(vecindex.Entry{
		ID: alphaTicketID, Kind: vecindex.KindTicketBody, Owner: "alpha", Vec: alphaVec,
	})
	mountB.TicketsIdx.Upsert(vecindex.Entry{
		ID: betaTicketID, Kind: vecindex.KindTicketBody, Owner: "beta", Vec: betaVec,
	})

	// Verify cross-talk impossibility: betaVec is 1024-dim and won't match
	// alpha's 768-dim index even if it were inserted there.
	wrongVec := make([]float32, 1024)
	hits := mountA.TicketsIdx.Search(wrongVec, vecindex.KindTicketBody, "alpha", 5)
	if len(hits) != 0 {
		t.Errorf("alpha index returned hits for 1024-dim query: %d", len(hits))
	}

	// mountProviderAndIndex routing: alpha → alpha's index + provider.
	provA, idxA := s.mountProviderAndIndex("alpha", indexKindTickets)
	if provA != mountA.Embed {
		t.Error("mountProviderAndIndex(alpha) returned wrong provider")
	}
	if idxA != mountA.TicketsIdx {
		t.Error("mountProviderAndIndex(alpha) returned wrong index")
	}
	provB, idxB := s.mountProviderAndIndex("beta", indexKindTickets)
	if provB != mountB.Embed {
		t.Error("mountProviderAndIndex(beta) returned wrong provider")
	}
	if idxB != mountB.TicketsIdx {
		t.Error("mountProviderAndIndex(beta) returned wrong index")
	}

	// Search alpha's index with a query embedded by alpha's provider — only
	// alphaTicketID should appear (beta's ticket lives in a different index
	// at a different dim, so it can't even be considered).
	queryAlpha, _ := provA.Embed(context.Background(), "alpha-only ticket text")
	hitsA := idxA.Search(queryAlpha, vecindex.KindTicketBody, "alpha", 5)
	if len(hitsA) != 1 || hitsA[0].ID != alphaTicketID {
		t.Errorf("alpha search hits = %+v; want one hit %s", hitsA, alphaTicketID)
	}
	queryBeta, _ := provB.Embed(context.Background(), "beta-only ticket text")
	hitsB := idxB.Search(queryBeta, vecindex.KindTicketBody, "beta", 5)
	if len(hitsB) != 1 || hitsB[0].ID != betaTicketID {
		t.Errorf("beta search hits = %+v; want one hit %s", hitsB, betaTicketID)
	}
}

// TestPerProjectMount_EvictionNilsIndexes confirms LRU eviction nils a
// mount's per-mount indexes (matching the contract the W2-T1 ticket calls
// out: indexes get nilled the same way Store is nilled today).
func TestPerProjectMount_EvictionNilsIndexes(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 1})
	tmp := t.TempDir()

	repoA := seedRepoWithProvider(t, tmp, "repoAlpha", "alpha", "ollama", "nomic-embed-text")
	repoB := seedRepoWithProvider(t, tmp, "repoBeta", "beta", "ollama", "nomic-embed-text")

	if _, err := s.RegisterProjectMount(context.Background(), repoA); err != nil {
		t.Fatalf("mount alpha: %v", err)
	}
	if _, err := s.RegisterProjectMount(context.Background(), repoB); err != nil {
		t.Fatalf("mount beta: %v", err)
	}

	// Cap is 1; older (alpha) should be evicted: Store nilled and indexes
	// nilled too. The mount entry survives so a re-resolve can re-mount.
	mountA := s.projectMounts["alpha"]
	if mountA == nil {
		t.Fatal("alpha mount missing post-eviction")
	}
	if mountA.Store != nil {
		t.Error("alpha Store not nilled by eviction")
	}
	if mountA.SummaryIdx != nil || mountA.TicketsIdx != nil ||
		mountA.LearningsIdx != nil || mountA.CommentsIdx != nil {
		t.Error("alpha per-mount indexes not nilled by eviction")
	}

	// Re-resolving alpha rebuilds Store + indexes.
	if _, err := s.ResolveProjectStore(context.Background(), "alpha"); err != nil {
		t.Fatalf("resolve alpha post-eviction: %v", err)
	}
	mountA = s.projectMounts["alpha"]
	if mountA.Store == nil {
		t.Error("alpha Store still nil after re-resolve")
	}
	if mountA.SummaryIdx == nil {
		t.Error("alpha SummaryIdx still nil after re-resolve")
	}
}
