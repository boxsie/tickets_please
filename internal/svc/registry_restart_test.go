package svc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"tickets_please/internal/config"
)

// TestRegistry_RestartSurvival proves the headline feature: a project
// registered before "shutdown" reappears as mounted in a freshly-built
// Service rooted at the same DataRoot.
func TestRegistry_RestartSurvival(t *testing.T) {
	dataRoot := t.TempDir()
	repoParent := t.TempDir()
	repoA := seedRepo(t, repoParent, "repoA", "proj-a")
	repoB := seedRepo(t, repoParent, "repoB", "proj-b")

	// First boot: mount two repos.
	{
		s := freshServiceNoDataDir(t, config.Config{DataRoot: dataRoot})
		if _, err := s.RegisterProjectMount(context.Background(), repoA); err != nil {
			t.Fatalf("register A: %v", err)
		}
		if _, err := s.RegisterProjectMount(context.Background(), repoB); err != nil {
			t.Fatalf("register B: %v", err)
		}
	}

	// Registry should now contain both paths.
	got, err := loadMountRegistry(dataRoot)
	if err != nil {
		t.Fatalf("loadMountRegistry: %v", err)
	}
	if !equalStrings(got, []string{repoA, repoB}) {
		t.Fatalf("registry = %v, want [%s %s]", got, repoA, repoB)
	}

	// Second boot: build a fresh Service against the same DataRoot. The
	// constructor's restore-from-registry pass should re-mount both.
	s2 := freshServiceNoDataDir(t, config.Config{DataRoot: dataRoot})

	// Both slugs should resolve through ResolveProjectStore without an
	// explicit RegisterProjectMount call — that's the whole point.
	stA, err := s2.ResolveProjectStore(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("post-restart resolve A: %v", err)
	}
	if got, want := stA.Root, filepath.Join(repoA, ".tickets_please"); got != want {
		t.Errorf("stA.Root = %q, want %q", got, want)
	}
	stB, err := s2.ResolveProjectStore(context.Background(), "proj-b")
	if err != nil {
		t.Fatalf("post-restart resolve B: %v", err)
	}
	if got, want := stB.Root, filepath.Join(repoB, ".tickets_please"); got != want {
		t.Errorf("stB.Root = %q, want %q", got, want)
	}
}

// TestRegistry_RestartTolerates_MissingRepo: a registry entry whose repo has
// vanished must NOT block startup. The remaining entries still mount.
func TestRegistry_RestartTolerates_MissingRepo(t *testing.T) {
	dataRoot := t.TempDir()
	repoParent := t.TempDir()
	repoA := seedRepo(t, repoParent, "repoA", "proj-a")

	// Pre-seed the registry with a real path AND a bogus one.
	if err := saveMountRegistry(dataRoot, []string{repoA, "/path/that/never/existed"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Boot — should not panic, should not error, should still mount repoA.
	s := freshServiceNoDataDir(t, config.Config{DataRoot: dataRoot})

	if _, err := s.ResolveProjectStore(context.Background(), "proj-a"); err != nil {
		t.Errorf("repoA failed to mount despite bogus sibling: %v", err)
	}
}

// TestRegistry_DeleteProjectRemovesFromRegistry: after DeleteProject succeeds
// the registry no longer carries that path, so a restart doesn't re-mount it.
//
// Uses the convention-compliant DataDir shape (`<repo>/.tickets_please`) so
// CreateProject's self-register path runs against a real repo parent.
func TestRegistry_DeleteProjectRemovesFromRegistry(t *testing.T) {
	dataRoot := t.TempDir()
	repoRoot := t.TempDir()
	dataDir := filepath.Join(repoRoot, ".tickets_please")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := freshServiceWithCfg(t, config.Config{DataRoot: dataRoot, DataDir: dataDir})

	authed, _ := authedCtx(t, s)
	proj, err := s.CreateProject(authed, "deletable", "Deletable", "tmp", validSummary())
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// CreateProject's self-register path adds the repo to the persistent
	// registry. Confirm it landed.
	before, _ := loadMountRegistry(dataRoot)
	if len(before) != 1 || before[0] != repoRoot {
		t.Fatalf("registry pre-delete = %v, want [%s]", before, repoRoot)
	}

	if err := s.DeleteProject(authed, proj.Slug); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	after, _ := loadMountRegistry(dataRoot)
	if len(after) != 0 {
		t.Errorf("registry post-delete = %v, want empty", after)
	}
}
