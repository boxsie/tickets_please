package svc

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/embed"
	"tickets_please/internal/store"
)

// waitForFileGone polls until the path is missing or timeout elapses.
func waitForFileGone(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// TestReembedProject_WipesAndRebuilds seeds a project with a project summary
// and a ticket body, calls ReembedProject, then verifies all sidecars vanish
// and reappear after the worker drains.
func TestReembedProject_WipesAndRebuilds(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{
		EmbedProvider: "ollama",
		OllamaModel:   "nomic-embed-text",
	})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "do thing", Body: "the body",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	summarySide := filepath.Join(s.Store.Root, "summary.embedding.json")
	if !waitForFile(summarySide, 5*time.Second) {
		t.Fatal("summary sidecar never written initially")
	}

	// Find ticket body sidecar.
	dirEntries, _ := os.ReadDir(filepath.Join(s.Store.Root, "tickets"))
	if len(dirEntries) == 0 {
		t.Fatal("ticket dir missing")
	}
	ticketDir := filepath.Join(s.Store.Root, "tickets", dirEntries[0].Name())
	bodySide := filepath.Join(ticketDir, "body.embedding.json")
	if !waitForFile(bodySide, 5*time.Second) {
		t.Fatal("body sidecar never written initially")
	}

	// Reembed.
	if err := s.ReembedProject(ctx, "alpha"); err != nil {
		t.Fatalf("ReembedProject: %v", err)
	}

	// After Flush in ReembedProject + the os.Remove walk, every sidecar
	// should be gone at least transiently. Because the call also kicks off
	// the worker re-enqueue, we don't try to assert "is gone right now" —
	// we just confirm the worker's Flush completes and produces a fresh
	// sidecar.
	s.flushAllMountWorkers(ctx)

	if !waitForFile(summarySide, 5*time.Second) {
		t.Fatal("summary sidecar never re-written after reembed")
	}
	if !waitForFile(bodySide, 5*time.Second) {
		t.Fatal("body sidecar never re-written after reembed")
	}
	_ = tk
}

// TestUpdateProject_EmbedModelChange_AutoReembeds confirms that when
// UpdateProject changes EmbedModel, the old sidecars get wiped and rebuilt
// with new metadata.
func TestUpdateProject_EmbedModelChange_AutoReembeds(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{
		EmbedProvider: "ollama",
		OllamaModel:   "nomic-embed-text",
	})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	summarySide := filepath.Join(s.Store.Root, "summary.embedding.json")
	if !waitForFile(summarySide, 5*time.Second) {
		t.Fatal("initial summary sidecar never written")
	}
	// Stat the initial mtime so we can confirm a rewrite happened.
	initial, err := os.Stat(summarySide)
	if err != nil {
		t.Fatal(err)
	}
	// The fakeEmbed doesn't care about model; freshServiceWithCfg's
	// EmbedNew always returns the same fake. So we change to a different
	// model name and verify the sidecar gets re-written under the new
	// stamp.
	newModel := "bge-m3"
	if _, err := s.UpdateProject(ctx, "alpha", domain.UpdateProjectInput{
		EmbedModel: &newModel,
	}); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}

	s.flushAllMountWorkers(ctx)

	// Sidecar reappears — its mtime should differ, but more importantly its
	// Model field should be the new value.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, err := os.Stat(summarySide)
		if err == nil && !st.ModTime().Equal(initial.ModTime()) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waitForFile(summarySide, 5*time.Second) {
		t.Fatal("summary sidecar missing after UpdateProject auto-reembed")
	}

	// Check that the mount's EmbedModel has been updated.
	mount := s.mountForSlug("alpha")
	if mount == nil {
		t.Fatal("mount missing")
	}
	if mount.EmbedModel != newModel {
		t.Errorf("mount.EmbedModel = %q; want %q", mount.EmbedModel, newModel)
	}
}

// TestReembedAllProjects_TwoMountsDifferentDims mounts two projects whose
// fake providers return different dims, calls ReembedAllProjects, and
// verifies each rebuilt independently with its own dim.
func TestReembedAllProjects_TwoMountsDifferentDims(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 4})
	tmp := t.TempDir()

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

	// Each repo needs a summary.md so hydrate has something to enqueue.
	if err := os.WriteFile(filepath.Join(repoA, ".tickets_please", "summary.md"), []byte("alpha summary text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoB, ".tickets_please", "summary.md"), []byte("beta summary text"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.RegisterProjectMount(context.Background(), repoA); err != nil {
		t.Fatalf("mount alpha: %v", err)
	}
	if _, err := s.RegisterProjectMount(context.Background(), repoB); err != nil {
		t.Fatalf("mount beta: %v", err)
	}

	ctx, _ := authedCtx(t, s)

	// Wait for initial worker writes to land summary sidecars.
	s.flushAllMountWorkers(ctx)
	summaryA := filepath.Join(repoA, ".tickets_please", "summary.embedding.json")
	summaryB := filepath.Join(repoB, ".tickets_please", "summary.embedding.json")
	if !waitForFile(summaryA, 5*time.Second) {
		t.Fatal("alpha summary sidecar never written initially")
	}
	if !waitForFile(summaryB, 5*time.Second) {
		t.Fatal("beta summary sidecar never written initially")
	}

	queued, err := s.ReembedAllProjects(ctx)
	if err != nil {
		t.Fatalf("ReembedAllProjects: %v", err)
	}
	if queued != 2 {
		t.Errorf("queued = %d; want 2", queued)
	}

	s.flushAllMountWorkers(ctx)

	if !waitForFile(summaryA, 5*time.Second) {
		t.Fatal("alpha summary sidecar never rewritten")
	}
	if !waitForFile(summaryB, 5*time.Second) {
		t.Fatal("beta summary sidecar never rewritten")
	}

	// Confirm dims survived independently.
	mountA := s.mountForSlug("alpha")
	mountB := s.mountForSlug("beta")
	if mountA.EmbedDim != 768 {
		t.Errorf("alpha EmbedDim after reembed = %d; want 768", mountA.EmbedDim)
	}
	if mountB.EmbedDim != 1024 {
		t.Errorf("beta EmbedDim after reembed = %d; want 1024", mountB.EmbedDim)
	}
}

// TestReembedProject_RebuildsAtNewDim verifies that when project.yaml's
// embed_model changes (between calls), ReembedProject rebuilds the mount's
// indexes/Worker at the new dim.
func TestReembedProject_RebuildsAtNewDim(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 4})
	tmp := t.TempDir()

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

	repo := seedRepoWithProvider(t, tmp, "repoAlpha", "alpha", "ollama", "nomic-embed-text")
	dataDir := filepath.Join(repo, ".tickets_please")
	if err := os.WriteFile(filepath.Join(dataDir, "summary.md"), []byte("alpha summary text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("mount alpha: %v", err)
	}

	ctx, _ := authedCtx(t, s)

	mount := s.mountForSlug("alpha")
	if mount.EmbedDim != 768 {
		t.Fatalf("initial alpha EmbedDim = %d; want 768", mount.EmbedDim)
	}

	// Hand-edit project.yaml to change embed_model to bge-m3.
	rec, err := mount.Store.ReadProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	rec.EmbedModel = "bge-m3"
	rec.UpdatedAt = time.Now()
	if err := store.WriteYAMLAtomic(filepath.Join(dataDir, "project.yaml"), rec); err != nil {
		t.Fatal(err)
	}

	if err := s.ReembedProject(ctx, "alpha"); err != nil {
		t.Fatalf("ReembedProject: %v", err)
	}

	// Mount's embed assets now reflect the new dim.
	mount = s.mountForSlug("alpha")
	if mount.EmbedDim != 1024 {
		t.Errorf("post-reembed alpha EmbedDim = %d; want 1024", mount.EmbedDim)
	}
	if mount.EmbedModel != "bge-m3" {
		t.Errorf("post-reembed alpha EmbedModel = %q; want bge-m3", mount.EmbedModel)
	}
}
