package svc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/config"
	"tickets_please/internal/store"
	"tickets_please/internal/vecindex"
)

// seedRepoWithSummary lays out a minimal mountable repo with a project.yaml
// and a summary.md but no sidecar. Returns the absolute repo path and the
// project id so tests can assert resident-index entries by id.
func seedRepoWithSummary(t *testing.T, parent, dirName, slug, summary string) (repo, projectID string) {
	t.Helper()
	repo = filepath.Join(parent, dirName)
	dataDir := filepath.Join(repo, ".tickets_please")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	projectID = uuid.NewString()
	now := time.Now()
	if err := store.WriteYAMLAtomic(filepath.Join(dataDir, "project.yaml"), &store.ProjectRecord{
		ID:        projectID,
		Slug:      slug,
		Name:      slug,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("write project.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "summary.md"), []byte(summary), 0o644); err != nil {
		t.Fatalf("write summary.md: %v", err)
	}
	return repo, projectID
}

// TestHydrate_StaleProvider_DropsAndReEmbeds plants a sidecar stamped with a
// provider name the mount never produces, mounts the project, and asserts:
// the stale file is gone, the worker writes a fresh sidecar with the right
// stamp, and the resident SummaryIdx has the project entry.
func TestHydrate_StaleProvider_DropsAndReEmbeds(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 4})
	tmp := t.TempDir()
	repo, projectID := seedRepoWithSummary(t, tmp, "alpha", "alpha",
		"alpha summary that is long enough to embed without complaint")

	// Pre-populate a sidecar stamped with the wrong provider — the mount's
	// fake provider Name() is "fake", so "definitely-not-fake" mismatches.
	side := filepath.Join(repo, ".tickets_please", "summary.embedding.json")
	bogusVec := make([]float32, 768)
	for i := range bogusVec {
		bogusVec[i] = 0.1
	}
	if err := vecindex.WriteSidecar(side, vecindex.Sidecar{
		Provider: "definitely-not-fake",
		Model:    "",
		Dim:      len(bogusVec),
		Vec:      bogusVec,
	}); err != nil {
		t.Fatalf("write stale sidecar: %v", err)
	}

	if _, err := s.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	mount := s.projectMounts["alpha"]
	if mount == nil {
		t.Fatal("alpha mount missing post-register")
	}
	// Worker is async — drain the queue so we can assert on the post-rebuild
	// state deterministically.
	mount.Worker.Flush(context.Background())

	sc, err := vecindex.ReadSidecar(side)
	if err != nil {
		t.Fatalf("re-read sidecar after hydrate: %v", err)
	}
	if sc.Provider != "fake" || sc.Model != "" {
		t.Errorf("rebuilt sidecar identity = (%q, %q), want (%q, %q)",
			sc.Provider, sc.Model, "fake", "")
	}
	if len(sc.Vec) != 768 {
		t.Errorf("rebuilt sidecar vec dim = %d, want 768", len(sc.Vec))
	}

	// Resident SummaryIdx should now hold the project entry, sourced from the
	// fresh embed (not the bogus 0.1-everywhere vec).
	entries := mount.SummaryIdx.Snapshot()
	var found *vecindex.Entry
	for i := range entries {
		if entries[i].ID == projectID {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("SummaryIdx missing project entry %s; have %d entries", projectID, len(entries))
	}
	// Re-embedded vec should NOT be the all-0.1 placeholder we wrote.
	if found.Vec[0] == 0.1 {
		t.Error("SummaryIdx vec looks like the bogus seeded one — staleness check didn't trigger")
	}
}

// TestHydrate_StaleModel_DropsAndReEmbeds is the same shape but with a model
// mismatch instead of provider mismatch. mount.EmbedModel resolves to "" in
// this test cfg, so any non-empty Model is stale.
func TestHydrate_StaleModel_DropsAndReEmbeds(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 4})
	tmp := t.TempDir()
	repo, _ := seedRepoWithSummary(t, tmp, "alpha", "alpha", "alpha body text long enough")

	side := filepath.Join(repo, ".tickets_please", "summary.embedding.json")
	if err := vecindex.WriteSidecar(side, vecindex.Sidecar{
		Provider: "fake",
		Model:    "ancient-model-name",
		Dim:      768,
		Vec:      make([]float32, 768),
	}); err != nil {
		t.Fatalf("write stale sidecar: %v", err)
	}

	if _, err := s.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	mount := s.projectMounts["alpha"]
	mount.Worker.Flush(context.Background())

	sc, err := vecindex.ReadSidecar(side)
	if err != nil {
		t.Fatalf("re-read sidecar after hydrate: %v", err)
	}
	if sc.Model != "" {
		t.Errorf("rebuilt sidecar Model = %q, want \"\"", sc.Model)
	}
	if mount.SummaryIdx.Len() == 0 {
		t.Error("SummaryIdx empty after stale-model re-embed")
	}
}

// TestHydrate_LegacyFlatArraySidecar_DropsAndReEmbeds writes a JSON array of
// floats — the pre-Sidecar-struct shape — to the sidecar path. ReadSidecar
// must fail to decode it; hydrate must treat that the same as stale and
// re-enqueue.
func TestHydrate_LegacyFlatArraySidecar_DropsAndReEmbeds(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 4})
	tmp := t.TempDir()
	repo, _ := seedRepoWithSummary(t, tmp, "alpha", "alpha",
		"alpha legacy-shape summary that needs re-embedding")

	side := filepath.Join(repo, ".tickets_please", "summary.embedding.json")
	legacy := make([]float32, 768)
	for i := range legacy {
		legacy[i] = 0.5
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy vec: %v", err)
	}
	if err := os.WriteFile(side, data, 0o644); err != nil {
		t.Fatalf("write legacy sidecar: %v", err)
	}
	// Sanity: confirm ReadSidecar refuses the legacy shape — that's the
	// behavioural contract this test depends on.
	if _, err := vecindex.ReadSidecar(side); err == nil {
		t.Fatal("ReadSidecar accepted a flat-array sidecar; this test's premise is wrong")
	}

	if _, err := s.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	mount := s.projectMounts["alpha"]
	mount.Worker.Flush(context.Background())

	sc, err := vecindex.ReadSidecar(side)
	if err != nil {
		t.Fatalf("re-read sidecar after hydrate: %v", err)
	}
	if sc.Provider != "fake" {
		t.Errorf("rebuilt Provider = %q, want \"fake\"", sc.Provider)
	}
	if mount.SummaryIdx.Len() == 0 {
		t.Error("SummaryIdx empty after legacy-shape re-embed")
	}
}

// TestHydrate_ColdClone_NoSidecarsEnqueues confirms the cold-clone path —
// project.yaml + summary.md but no sidecar — still triggers the enqueue
// branch after the staleness rework. Worker.Flush forces the rebuild, and
// the sidecar should appear on disk with the mount's stamp.
func TestHydrate_ColdClone_NoSidecarsEnqueues(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 4})
	tmp := t.TempDir()
	repo, projectID := seedRepoWithSummary(t, tmp, "alpha", "alpha",
		"cold-clone summary, no sidecar exists at mount time")

	side := filepath.Join(repo, ".tickets_please", "summary.embedding.json")
	if _, err := os.Stat(side); err == nil {
		t.Fatal("sidecar already on disk before mount; test setup is wrong")
	}

	if _, err := s.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	mount := s.projectMounts["alpha"]
	mount.Worker.Flush(context.Background())

	sc, err := vecindex.ReadSidecar(side)
	if err != nil {
		t.Fatalf("expected a fresh sidecar after cold-clone hydrate: %v", err)
	}
	if sc.Provider != "fake" {
		t.Errorf("Provider = %q, want \"fake\"", sc.Provider)
	}
	if len(sc.Vec) != 768 {
		t.Errorf("Vec dim = %d, want 768", len(sc.Vec))
	}
	entries := mount.SummaryIdx.Snapshot()
	hit := false
	for _, e := range entries {
		if e.ID == projectID {
			hit = true
			break
		}
	}
	if !hit {
		t.Errorf("SummaryIdx missing project %s post-hydrate; have %d entries", projectID, len(entries))
	}
}
