package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// freshStore returns a Store rooted at t.TempDir() with sensible test
// defaults (auto_commit off, fsnotify on, 5s lock timeout).
func freshStore(t *testing.T) *Store {
	t.Helper()
	cfg := config.Config{
		DataDir:            t.TempDir(),
		AutoCommit:         false,
		LockTimeoutSeconds: 5,
		FsnotifyEnabled:    true,
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestNew_CreatesSkeletonDirs(t *testing.T) {
	s := freshStore(t)
	// Post-T003 skeleton: only .staging/ at the data-dir root.
	// agents/ is no longer created by Store.New — it lives in the central
	// AgentStore (see TestNewAgentStore_CreatesDirs for the equivalent check).
	path := filepath.Join(s.Root, ".staging")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("missing .staging: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf(".staging is not a dir")
	}
	// agents/ must NOT be created by the project Store.
	if _, err := os.Stat(filepath.Join(s.Root, "agents")); !os.IsNotExist(err) {
		t.Fatalf("agents/ should not be created by Store.New (post-T003), got err=%v", err)
	}
	// `projects/` must NOT be created — that dir is part of the old layout.
	if _, err := os.Stat(filepath.Join(s.Root, "projects")); !os.IsNotExist(err) {
		t.Fatalf("projects/ dir should not be created post-flatten, got err=%v", err)
	}
}

func TestNewAgentStore_CreatesDirs(t *testing.T) {
	root := t.TempDir()
	as, err := NewAgentStore(root, 5)
	if err != nil {
		t.Fatalf("NewAgentStore: %v", err)
	}
	for _, sub := range []string{"agents", ".staging"} {
		path := filepath.Join(as.Root, sub)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing %s: %v", sub, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a dir", sub)
		}
	}
}

func TestStageOp_RoundTrip_WriteRenameRemove(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()

	// Pre-populate a directory we'll RenameDir, and a file we'll RemovePath.
	srcDir := filepath.Join(s.Root,"tickets", "001-foo")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "ticket.yaml"), []byte("id: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stragglerDir := filepath.Join(s.Root,"phases", "001-old")
	if err := os.MkdirAll(stragglerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stragglerDir, "phase.yaml"), []byte("id: y\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	op, err := s.BeginOp()
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Write("project.yaml", []byte("id: alpha\n")); err != nil {
		t.Fatal(err)
	}
	if err := op.Write("summary.md", []byte("project summary\n")); err != nil {
		t.Fatal(err)
	}
	if err := op.RenameDir("tickets/001-foo", "tickets/001-bar"); err != nil {
		t.Fatal(err)
	}
	if err := op.RemovePath("phases/001-old"); err != nil {
		t.Fatal(err)
	}
	stagingPath := op.dir
	if err := op.Commit(ctx, LockProject("alpha"), nil, ""); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Writes landed at final paths.
	for _, rel := range []string{"project.yaml", "summary.md"} {
		if _, err := os.Stat(filepath.Join(s.Root, rel)); err != nil {
			t.Errorf("expected %s present: %v", rel, err)
		}
	}
	// Rename applied.
	if _, err := os.Stat(filepath.Join(s.Root, "tickets/001-bar/ticket.yaml")); err != nil {
		t.Errorf("expected renamed dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "tickets/001-foo")); !os.IsNotExist(err) {
		t.Errorf("expected old dir gone, got err=%v", err)
	}
	// Remove applied.
	if _, err := os.Stat(filepath.Join(s.Root, "phases/001-old")); !os.IsNotExist(err) {
		t.Errorf("expected removed dir gone, got err=%v", err)
	}
	// Staging dir cleaned.
	if _, err := os.Stat(stagingPath); !os.IsNotExist(err) {
		t.Errorf("expected staging dir gone, got err=%v", err)
	}
}

func TestStageOp_OpsAppliedInOrder(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()

	// Op order: Write a then RenameDir a→b. Must produce only `b` afterwards.
	op, err := s.BeginOp()
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Write("dir/a/file.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := op.RenameDir("dir/a", "dir/b"); err != nil {
		t.Fatal(err)
	}
	if err := op.Commit(ctx, LockGlobal, nil, ""); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(s.Root, "dir/b/file.txt")); err != nil {
		t.Fatalf("expected dir/b/file.txt: %v", err)
	} else if string(data) != "hello" {
		t.Fatalf("contents: %q", data)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "dir/a")); !os.IsNotExist(err) {
		t.Fatalf("dir/a should be gone: %v", err)
	}
}

func TestStageOp_AbortLeavesNothing(t *testing.T) {
	s := freshStore(t)
	op, err := s.BeginOp()
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Write("foo.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	op.Abort()
	if _, err := os.Stat(op.dir); !os.IsNotExist(err) {
		t.Fatalf("staging dir should be gone, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "foo.txt")); !os.IsNotExist(err) {
		t.Fatalf("final path should not exist, got %v", err)
	}
}

func TestStageOp_StagingPersistsBeforeCommit(t *testing.T) {
	// Mimics "process killed between Write and Commit": Write, then never
	// call Commit. The integrity check at next startup should surface the
	// residual op-id as a warning.
	s := freshStore(t)
	op, err := s.BeginOp()
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Write("project.yaml", []byte("id: p\n")); err != nil {
		t.Fatal(err)
	}
	stagedPath := filepath.Join(op.dir, "project.yaml")
	if _, err := os.Stat(stagedPath); err != nil {
		t.Fatalf("expected staged file: %v", err)
	}
	// Final path must NOT exist.
	if _, err := os.Stat(filepath.Join(s.Root, "project.yaml")); !os.IsNotExist(err) {
		t.Fatalf("final path should not exist: %v", err)
	}
	// Integrity walk must surface the residual op.
	warnings, _, err := s.Integrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range warnings {
		if filepath.Dir(w.Path) == ".staging" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected residual staging warning, got %+v", warnings)
	}
}

func TestStageOp_RejectsBadRel(t *testing.T) {
	s := freshStore(t)
	op, _ := s.BeginOp()
	defer op.Abort()
	for _, bad := range []string{"", "/abs", "../up", "a/../../b"} {
		if err := op.Write(bad, nil); err == nil {
			t.Errorf("expected reject for %q", bad)
		}
	}
}

func TestMarkdownRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "comment.md")
	rec := CommentRecord{
		ID:        "c1",
		TicketID:  "t1",
		Kind:      domain.CommentKindUser,
		CreatedAt: time.Date(2026, 5, 2, 14, 11, 9, 0, time.UTC),
	}
	body := "Hello, **world**.\n\nMore text."
	if err := WriteMarkdown(path, rec, body); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	got := CommentRecord{}
	gotBody, err := DecodeMarkdownInto(path, &got)
	if err != nil {
		t.Fatalf("DecodeMarkdownInto: %v", err)
	}
	if got.ID != rec.ID || got.TicketID != rec.TicketID || got.Kind != rec.Kind {
		t.Fatalf("frontmatter mismatch: got %+v want %+v", got, rec)
	}
	if !got.CreatedAt.Equal(rec.CreatedAt) {
		t.Fatalf("created_at mismatch: %v vs %v", got.CreatedAt, rec.CreatedAt)
	}
	// Body round-trips with at most a single trailing newline added; strip
	// for comparison.
	gotBody = trimTrailingNL(gotBody)
	if gotBody != body {
		t.Fatalf("body mismatch:\n got: %q\n want: %q", gotBody, body)
	}
}

func TestReadMarkdown_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(path, []byte("just markdown\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fm, body, err := ReadMarkdown(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(fm) != 0 {
		t.Fatalf("expected empty fm, got %+v", fm)
	}
	if body != "just markdown\n" {
		t.Fatalf("body: %q", body)
	}
}

func trimTrailingNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func TestRegisterAgent_Uniqueness(t *testing.T) {
	// AgentStore is now the home of agent operations; Store no longer has
	// RegisterAgent / WriteAgentRecord methods.
	as, err := NewAgentStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewAgentStore: %v", err)
	}
	ctx := context.Background()
	now := time.Now()
	rec1 := &AgentRecord{
		ID:        "agent-1",
		Key:       "claude:run-1",
		Name:      "Claude One",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := as.RegisterAgent(ctx, rec1); err != nil {
		t.Fatalf("first RegisterAgent: %v", err)
	}

	// Second active session with same key → ErrAlreadyExists.
	rec2 := &AgentRecord{
		ID:        "agent-2",
		Key:       "claude:run-1",
		Name:      "Claude Two",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	err = as.RegisterAgent(ctx, rec2)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}

	// Expire the first by overwriting its file with an expired timestamp.
	rec1.ExpiresAt = now.Add(-time.Minute)
	if err := as.WriteAgentRecord(rec1); err != nil {
		t.Fatal(err)
	}
	// Now rec2 should succeed.
	if err := as.RegisterAgent(ctx, rec2); err != nil {
		t.Fatalf("post-expiry RegisterAgent: %v", err)
	}
}

func TestWalkComments_ChronologicalOrder(t *testing.T) {
	s := freshStore(t)
	ticketDir := filepath.Join(s.Root, "tickets/001-bar")
	commentsDir := filepath.Join(ticketDir, "comments")
	if err := os.MkdirAll(commentsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Three comments with timestamps in mixed order; filenames encode
	// timestamps so a string sort yields chronological order.
	stamps := []struct {
		name string
		t    time.Time
	}{
		{"20260502T140000Z-c003-user.md", time.Date(2026, 5, 2, 14, 0, 0, 0, time.UTC)},
		{"20260502T120000Z-c001-user.md", time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)},
		{"20260502T130000Z-c002-user.md", time.Date(2026, 5, 2, 13, 0, 0, 0, time.UTC)},
	}
	for _, s := range stamps {
		rec := CommentRecord{ID: s.name, TicketID: "t", Kind: domain.CommentKindUser, CreatedAt: s.t}
		if err := WriteMarkdown(filepath.Join(commentsDir, s.name), rec, "x"); err != nil {
			t.Fatal(err)
		}
	}
	var seen []time.Time
	if err := s.WalkComments(ticketDir, func(rec *CommentRecord, _ string) error {
		seen = append(seen, rec.CreatedAt)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 comments, got %d", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i].Before(seen[i-1]) {
			t.Fatalf("comments out of order at %d: %v then %v", i, seen[i-1], seen[i])
		}
	}
}

func TestWithProjectLock_SerializesSameSlug(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	var counter int32
	var maxConcurrent int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.WithProjectLock(ctx, "p", func() error {
				cur := atomic.AddInt32(&counter, 1)
				// Track max-concurrent — must stay at 1.
				for {
					prev := atomic.LoadInt32(&maxConcurrent)
					if cur <= prev || atomic.CompareAndSwapInt32(&maxConcurrent, prev, cur) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				atomic.AddInt32(&counter, -1)
				return nil
			})
			if err != nil {
				t.Errorf("lock: %v", err)
			}
		}()
	}
	wg.Wait()
	if maxConcurrent != 1 {
		t.Fatalf("max concurrent holders = %d, want 1", maxConcurrent)
	}
}

// TestWithProjectLock_DifferentSlugsDontBlock was removed in the flat-layout
// refactor: a Store is now rooted at a single project, so all `LockProject`
// calls (regardless of the slug arg) resolve to `<Root>/.lock`. Cross-project
// non-blocking is now a property between *different Stores*, exercised by
// the integration tests in svc/concurrency_test.go.

func TestWithProjectLock_TimeoutFires(t *testing.T) {
	cfg := config.Config{
		DataDir:            t.TempDir(),
		AutoCommit:         false,
		LockTimeoutSeconds: 1,
		FsnotifyEnabled:    true,
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	holding := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_ = s.WithProjectLock(context.Background(), "p", func() error {
			close(holding)
			<-release
			return nil
		})
		close(done)
	}()
	<-holding

	start := time.Now()
	err = s.WithProjectLock(context.Background(), "p", func() error { return nil })
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("timeout fired at unexpected time: %v", elapsed)
	}
	close(release)
	<-done
}
