package cache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// freshStore mints a Store rooted at t.TempDir() with sane test defaults.
// fsnotify is on by default (mirrors prod) so cross-process staleness tests
// work without per-test config gymnastics.
func freshStore(t *testing.T, fsnotifyOn bool) *store.Store {
	t.Helper()
	cfg := config.Config{
		DataDir:            t.TempDir(),
		AutoCommit:         false,
		LockTimeoutSeconds: 5,
		FsnotifyEnabled:    fsnotifyOn,
	}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return s
}

// seedProject lays down `<Root>/{project.yaml,summary.md}` on disk without
// going through StageOp — tests want a populated tree without auth/middleware
// noise. After the flat-layout refactor a Store is single-project, so calling
// seedProject on the same Store twice will overwrite the previous project.
// Tests that need multiple projects build multiple Stores via freshStore.
func seedProject(t *testing.T, st *store.Store, slug, name string) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now()
	rec := &store.ProjectRecord{
		ID:        id,
		Slug:      slug,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}
	dir := st.Root
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteYAMLAtomic(filepath.Join(dir, "project.yaml"), rec); err != nil {
		t.Fatal(err)
	}
	summary := strings.Repeat("ok ", 100) // > 200 chars
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(summary+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCache_Get_LazyLoadPopulatesProject(t *testing.T) {
	st := freshStore(t, false)
	id := seedProject(t, st, "foo", "Foo")

	c := New(st, config.Config{})
	if c.Len() != 0 {
		t.Fatalf("expected empty cache, got %d", c.Len())
	}

	lp, handle, err := c.Get(context.Background(), "foo")
	if err != nil {
		t.Fatal(err)
	}
	if lp.Project == nil || lp.Project.Slug != "foo" || lp.Project.ID != id {
		t.Fatalf("unexpected project: %+v", lp.Project)
	}
	if handle == "" {
		t.Fatal("expected non-empty diagnostic handle")
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 cached entry, got %d", c.Len())
	}

	// Second call should reuse the same entry (handle stable).
	_, handle2, err := c.Get(context.Background(), "foo")
	if err != nil {
		t.Fatal(err)
	}
	if handle2 != handle {
		t.Fatalf("handle drift: %s vs %s", handle, handle2)
	}
}

func TestCache_Get_ResolvesByID(t *testing.T) {
	st := freshStore(t, false)
	id := seedProject(t, st, "foo", "Foo")

	c := New(st, config.Config{})
	lp, _, err := c.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if lp.Project.Slug != "foo" {
		t.Fatalf("id lookup mismatch: %s", lp.Project.Slug)
	}
}

func TestCache_Get_NotFound(t *testing.T) {
	st := freshStore(t, false)
	c := New(st, config.Config{})
	_, _, err := c.Get(context.Background(), "missing")
	if !domain.IsNotFound(err) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCache_Evict_ClosesWatcherAndRemoves(t *testing.T) {
	st := freshStore(t, true)
	seedProject(t, st, "foo", "Foo")

	c := New(st, config.Config{})
	if _, _, err := c.Get(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 loaded, got %d", c.Len())
	}
	c.Evict("foo")
	if c.Len() != 0 {
		t.Fatalf("expected 0 after evict, got %d", c.Len())
	}
	// Idempotent.
	c.Evict("foo")
	c.Evict("never-loaded")
}

func TestCache_SweepIdle_EvictsExpired(t *testing.T) {
	st := freshStore(t, false)
	seedProject(t, st, "foo", "Foo")

	c := New(st, config.Config{ProjectIdleMinutes: 1})
	lp, _, err := c.Get(context.Background(), "foo")
	if err != nil {
		t.Fatal(err)
	}
	// Backdate access to push past TTL.
	lp.LastAccessAt = time.Now().Add(-2 * time.Minute)

	c.SweepIdle(time.Now())
	if c.Len() != 0 {
		t.Fatalf("expected idle sweep to evict, got %d remaining", c.Len())
	}

	// Next Get reloads cleanly.
	if _, _, err := c.Get(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 1 {
		t.Fatalf("expected reload, got %d", c.Len())
	}
}

func TestCache_LRU_EvictsOldestBeyondMax(t *testing.T) {
	// Post-flatten a Store is single-project, so the cache's slug-keyed LRU
	// can hold at most one entry per Store. We exercise the cap-and-evict
	// path by hand-forcing a second slug into the loaded map (the upcoming
	// multi-Store registry ticket will exercise it via real loads against
	// many Stores).
	st := freshStore(t, false)
	seedProject(t, st, "alpha", "Alpha")

	c := New(st, config.Config{MaxLoadedProjects: 1})

	if _, _, err := c.Get(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}

	// Inject a fake older entry so the LRU sweep has something to drop.
	c.mu.Lock()
	c.loaded["zombie"] = &LoadedProject{LastAccessAt: time.Now().Add(-time.Hour)}
	c.mu.Unlock()

	c.SweepIdle(time.Now())

	c.mu.Lock()
	_, hasZombie := c.loaded["zombie"]
	_, hasAlpha := c.loaded["alpha"]
	c.mu.Unlock()
	if hasZombie {
		t.Fatal("expected stale zombie entry to be LRU-evicted")
	}
	if !hasAlpha {
		t.Fatal("expected fresh alpha entry to survive")
	}
}

func TestCache_StaleFlagTriggersReload(t *testing.T) {
	st := freshStore(t, true)
	seedProject(t, st, "foo", "Foo")

	c := New(st, config.Config{})
	lp1, _, err := c.Get(context.Background(), "foo")
	if err != nil {
		t.Fatal(err)
	}

	// Manually flip Stale; next Get should evict + reload.
	lp1.Stale.Store(true)

	lp2, _, err := c.Get(context.Background(), "foo")
	if err != nil {
		t.Fatal(err)
	}
	if lp1 == lp2 {
		t.Fatal("expected reload to produce a new LoadedProject; got the stale one back")
	}
}

func TestCache_FsnotifyFlipsStaleOnDiskWrite(t *testing.T) {
	st := freshStore(t, true)
	seedProject(t, st, "foo", "Foo")

	c := New(st, config.Config{})
	lp, _, err := c.Get(context.Background(), "foo")
	if err != nil {
		t.Fatal(err)
	}
	if lp.watcher == nil {
		t.Fatal("expected watcher attached when fsnotify enabled")
	}

	// Write to project.yaml from "another process" (just direct disk write).
	rec, err := st.ReadProject("foo")
	if err != nil {
		t.Fatal(err)
	}
	rec.Description = "outside-process edit"
	if err := store.WriteYAMLAtomic(filepath.Join(st.Root, "project.yaml"), rec); err != nil {
		t.Fatal(err)
	}

	// Wait up to 2s for the debounced fsnotify event.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if lp.Stale.Load() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !lp.Stale.Load() {
		t.Fatal("expected Stale to flip after disk write")
	}

	// Next Get should evict + reload, producing a different *LoadedProject.
	lp2, _, err := c.Get(context.Background(), "foo")
	if err != nil {
		t.Fatal(err)
	}
	if lp == lp2 {
		t.Fatal("expected reload after stale flip")
	}
	if lp2.Project.Description != "outside-process edit" {
		t.Fatalf("expected reloaded project to reflect disk edit, got %q", lp2.Project.Description)
	}
}

func TestCache_CloseAll_ReleasesWatchers(t *testing.T) {
	// Single-project Store post-flatten; CloseAll still has to drain whatever
	// is loaded. The next-ticket multi-Store registry will broaden this.
	st := freshStore(t, true)
	seedProject(t, st, "foo", "Foo")

	c := New(st, config.Config{})
	if _, _, err := c.Get(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 loaded before CloseAll, got %d", c.Len())
	}
	c.CloseAll()
	if c.Len() != 0 {
		t.Fatalf("expected 0 after CloseAll, got %d", c.Len())
	}
}

func TestCache_RunEvictor_StopsOnContextCancel(t *testing.T) {
	st := freshStore(t, false)
	c := New(st, config.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.RunEvictor(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Fatal("RunEvictor did not return after ctx cancel")
	}
}

func TestSplitCompletionSections_ParsesAllThree(t *testing.T) {
	md := "## Testing Evidence\nran the suite\n\n## Work Summary\nwired the cache\n\n## Learnings\nwatch closing matters\n"
	te, ws, ln := splitCompletionSections(md)
	if te != "ran the suite" || ws != "wired the cache" || ln != "watch closing matters" {
		t.Fatalf("split mismatch: te=%q ws=%q ln=%q", te, ws, ln)
	}
}
