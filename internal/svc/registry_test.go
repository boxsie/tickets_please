package svc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/config"
	"tickets_please/internal/store"
)

// seedRepo creates a fake repo on disk: <root>/<name>/.tickets_please/project.yaml.
// Returns the absolute repo path that RegisterProjectMount expects.
func seedRepo(t *testing.T, parent, dirName, slug string) string {
	t.Helper()
	repo := filepath.Join(parent, dirName)
	dataDir := filepath.Join(repo, ".tickets_please")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	now := time.Now()
	rec := &store.ProjectRecord{
		ID:        uuid.NewString(),
		Slug:      slug,
		Name:      slug,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.WriteYAMLAtomic(filepath.Join(dataDir, "project.yaml"), rec); err != nil {
		t.Fatalf("write project.yaml: %v", err)
	}
	return repo
}

// freshServiceNoDataDir builds a Service with no cfg.DataDir so the eager-mount
// path is skipped — useful for tests that only exercise RegisterProjectMount.
func freshServiceNoDataDir(t *testing.T, cfg config.Config) *Service {
	t.Helper()
	if cfg.DataRoot == "" {
		cfg.DataRoot = t.TempDir()
	}
	if cfg.LockTimeoutSeconds == 0 {
		cfg.LockTimeoutSeconds = 5
	}
	if cfg.AgentSessionTTLMinutes == 0 {
		cfg.AgentSessionTTLMinutes = 60
	}
	if cfg.AgentSessionMaxMinutes == 0 {
		cfg.AgentSessionMaxMinutes = 240
	}
	// cfg.DataDir intentionally empty: skip eager mount. We still need a
	// non-empty DataDir for store.New (which the constructor calls); use a
	// scratch tempdir that holds no project.yaml so the eager-mount path
	// silently no-ops.
	if cfg.DataDir == "" {
		cfg.DataDir = t.TempDir()
	}
	s, err := NewWithEmbed(cfg, newFakeEmbed())
	if err != nil {
		t.Fatalf("NewWithEmbed: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestRegistry_RegisterAndResolveTwoProjects(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{})
	tmp := t.TempDir()
	repoA := seedRepo(t, tmp, "repoA", "proj-a")
	repoB := seedRepo(t, tmp, "repoB", "proj-b")

	slugA, err := s.RegisterProjectMount(context.Background(), repoA)
	if err != nil {
		t.Fatalf("register A: %v", err)
	}
	if slugA != "proj-a" {
		t.Fatalf("slugA = %q, want proj-a", slugA)
	}
	slugB, err := s.RegisterProjectMount(context.Background(), repoB)
	if err != nil {
		t.Fatalf("register B: %v", err)
	}
	if slugB != "proj-b" {
		t.Fatalf("slugB = %q, want proj-b", slugB)
	}

	stA, err := s.ResolveProjectStore(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("resolve A: %v", err)
	}
	if got, want := stA.Root, filepath.Join(repoA, ".tickets_please"); got != want {
		t.Fatalf("stA.Root = %q, want %q", got, want)
	}
	stB, err := s.ResolveProjectStore(context.Background(), "proj-b")
	if err != nil {
		t.Fatalf("resolve B: %v", err)
	}
	if got, want := stB.Root, filepath.Join(repoB, ".tickets_please"); got != want {
		t.Fatalf("stB.Root = %q, want %q", got, want)
	}
	if stA == stB {
		t.Fatal("expected distinct Store pointers per mount")
	}
}

func TestRegistry_SlugCollisionDifferentRepoErrors(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{})
	tmp := t.TempDir()
	repoA := seedRepo(t, tmp, "repoA", "shared-slug")
	repoB := seedRepo(t, tmp, "repoB", "shared-slug")

	if _, err := s.RegisterProjectMount(context.Background(), repoA); err != nil {
		t.Fatalf("register A: %v", err)
	}
	_, err := s.RegisterProjectMount(context.Background(), repoB)
	if err == nil {
		t.Fatal("expected slug collision error, got nil")
	}
	if !strings.Contains(err.Error(), "already mounted") {
		t.Fatalf("err = %v, want 'already mounted'", err)
	}
}

func TestRegistry_SameRepoDifferentUUIDErrors(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{})
	tmp := t.TempDir()
	repo := seedRepo(t, tmp, "repo", "twin-slug")
	if _, err := s.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("first register: %v", err)
	}
	// Rewrite project.yaml in place with a different UUID, same slug.
	yamlPath := filepath.Join(repo, ".tickets_please", "project.yaml")
	rec := &store.ProjectRecord{
		ID:        uuid.NewString(),
		Slug:      "twin-slug",
		Name:      "twin",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.WriteYAMLAtomic(yamlPath, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterProjectMount(context.Background(), repo); err == nil {
		t.Fatal("expected slug collision when project UUID changes under same repo")
	}
}

func TestRegistry_RegisterIdempotent(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{})
	tmp := t.TempDir()
	repoA := seedRepo(t, tmp, "repoA", "proj-a")

	slug1, err := s.RegisterProjectMount(context.Background(), repoA)
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	slug2, err := s.RegisterProjectMount(context.Background(), repoA)
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	if slug1 != slug2 {
		t.Fatalf("idempotent register returned different slugs: %q vs %q", slug1, slug2)
	}
	count := 0
	if err := s.WalkProjectMounts(func(_ string, _ *ProjectMount) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("mount count = %d, want 1", count)
	}
}

func TestRegistry_LRUEvictionAndSilentRemount(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 2})
	tmp := t.TempDir()
	repoA := seedRepo(t, tmp, "repoA", "proj-a")
	repoB := seedRepo(t, tmp, "repoB", "proj-b")
	repoC := seedRepo(t, tmp, "repoC", "proj-c")

	if _, err := s.RegisterProjectMount(context.Background(), repoA); err != nil {
		t.Fatal(err)
	}
	// Sleep a hair to ensure LastTouchedAt ordering is stable.
	time.Sleep(2 * time.Millisecond)
	if _, err := s.RegisterProjectMount(context.Background(), repoB); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := s.RegisterProjectMount(context.Background(), repoC); err != nil {
		t.Fatal(err)
	}

	// proj-a was the oldest non-keep mount when we added proj-c → it should be
	// evicted (Store nil, RepoPath retained).
	s.mountsMu.Lock()
	mountA := s.projectMounts["proj-a"]
	s.mountsMu.Unlock()
	if mountA == nil {
		t.Fatal("proj-a missing from registry after eviction")
	}
	if mountA.Store != nil {
		t.Fatalf("expected proj-a evicted (Store nil), got %v", mountA.Store)
	}
	if mountA.RepoPath != repoA {
		t.Fatalf("evicted mount RepoPath = %q, want %q", mountA.RepoPath, repoA)
	}

	// Resolving proj-a should re-mount silently.
	stA, err := s.ResolveProjectStore(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("resolve evicted proj-a: %v", err)
	}
	if stA == nil {
		t.Fatal("re-mounted store is nil")
	}
	if got := stA.Root; got != filepath.Join(repoA, ".tickets_please") {
		t.Fatalf("re-mounted Store.Root = %q", got)
	}
}

func TestRegistry_ResolveUnmountedReturnsError(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{})
	_, err := s.ResolveProjectStore(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error for unmounted slug")
	}
	if !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("err = %v, want 'not mounted'", err)
	}
}

func TestRegistry_ConcurrentRegisterAndResolve(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 32})
	tmp := t.TempDir()

	const n = 10
	repos := make([]string, n)
	slugs := make([]string, n)
	for i := 0; i < n; i++ {
		slugs[i] = fmt.Sprintf("proj-%d", i)
		repos[i] = seedRepo(t, tmp, fmt.Sprintf("repo-%d", i), slugs[i])
	}

	var wg sync.WaitGroup
	errCh := make(chan error, n*4)

	// Half the goroutines register; the other half try to resolve concurrently
	// (some resolves race the registrations and may legitimately get
	// "not mounted" — they retry once).
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if _, err := s.RegisterProjectMount(context.Background(), repos[idx]); err != nil {
				errCh <- fmt.Errorf("register %d: %w", idx, err)
			}
		}(i)
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			deadline := time.Now().Add(2 * time.Second)
			for {
				if _, err := s.ResolveProjectStore(context.Background(), slugs[idx]); err == nil {
					return
				}
				if time.Now().After(deadline) {
					errCh <- fmt.Errorf("resolve %d timed out", idx)
					return
				}
				time.Sleep(time.Millisecond)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	// Verify all 10 are registered.
	count := 0
	if err := s.WalkProjectMounts(func(_ string, _ *ProjectMount) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("mount count = %d, want %d", count, n)
	}
}

func TestRegistry_EagerMountFromCfgDataDir(t *testing.T) {
	tmp := t.TempDir()
	repo := seedRepo(t, tmp, "myrepo", "eager-slug")
	cfg := config.Config{
		DataDir:                filepath.Join(repo, ".tickets_please"),
		DataRoot:               t.TempDir(),
		LockTimeoutSeconds:     5,
		AgentSessionTTLMinutes: 60,
		AgentSessionMaxMinutes: 240,
	}
	s, err := NewWithEmbed(cfg, newFakeEmbed())
	if err != nil {
		t.Fatalf("NewWithEmbed: %v", err)
	}
	t.Cleanup(s.Close)

	// Eager mount should have populated the registry off cfg.DataDir's parent.
	st, err := s.ResolveProjectStore(context.Background(), "eager-slug")
	if err != nil {
		t.Fatalf("resolve eager-mounted slug: %v", err)
	}
	if st == nil || st.Root != cfg.DataDir {
		t.Fatalf("eager-mounted Store.Root = %v, want %s", st, cfg.DataDir)
	}
}

func TestRegistry_NonAbsoluteRepoPathErrors(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{})
	if _, err := s.RegisterProjectMount(context.Background(), "relative/path"); err == nil {
		t.Fatal("expected error for non-absolute repoPath")
	}
}
