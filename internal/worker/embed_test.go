package worker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tickets_please/internal/vecindex"
)

// freshIndexes returns a fresh Indexes bundle with empty resident indexes.
func freshIndexes() Indexes {
	return Indexes{
		Summaries: vecindex.New(),
		Tickets:   vecindex.New(),
		Learnings: vecindex.New(),
		Comments:  vecindex.New(),
	}
}

// silentLogger discards everything so test logs stay clean.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// runWorker starts a worker, returns a stop function that cancels and waits
// for drain. Tests should defer it.
func runWorker(t *testing.T, p *fakeProvider, idx Indexes) (*Worker, func()) {
	t.Helper()
	w := New(context.Background(), p, "fake-model", idx, 16, silentLogger())
	return w, func() {
		w.Stop(context.Background())
	}
}

// waitFor polls fn at 5ms intervals up to timeout. Returns true on first
// fn() == true.
func waitFor(timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fn()
}

func TestWorker_ProjectSummary_WritesSidecarAndUpserts(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "summary.md")
	side := filepath.Join(dir, "summary.embedding.json")
	if err := os.WriteFile(src, []byte("about the project"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := freshIndexes()
	w, stop := runWorker(t, newFake(), idx)
	defer stop()

	w.Enqueue(Job{
		Kind:        JobProjectSummary,
		SourcePath:  src,
		SidecarPath: side,
		EntryID:     "p1",
		Owner:       "alpha",
		Text:        "hello world",
	})

	if !waitFor(2*time.Second, func() bool {
		_, err := os.Stat(side)
		return err == nil && idx.Summaries.Len() == 1
	}) {
		t.Fatalf("sidecar+upsert never landed: side err=%v idx=%d",
			fileExists(side), idx.Summaries.Len())
	}

	sc, err := vecindex.ReadSidecar(side)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if len(sc.Vec) != 768 {
		t.Errorf("sidecar dim = %d, want 768", len(sc.Vec))
	}
	if sc.Dim != 768 {
		t.Errorf("sidecar Dim field = %d, want 768", sc.Dim)
	}
	if sc.Provider == "" {
		t.Errorf("sidecar Provider empty; want fake provider Name()")
	}
	if sc.Model != "fake-model" {
		t.Errorf("sidecar Model = %q; want fake-model", sc.Model)
	}
}

func TestWorker_AllKindsRouteToCorrectIndex(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, text string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(text), 0o644)
		return p
	}
	idx := freshIndexes()
	w, stop := runWorker(t, newFake(), idx)
	defer stop()

	cases := []struct {
		kind     JobKind
		text     string
		entryID  string
		expected *vecindex.Index
	}{
		{JobProjectSummary, "summary", "p1", idx.Summaries},
		{JobTicketBody, "title\n\nbody", "t1", idx.Tickets},
		{JobTicketLearnings, "learnings text", "t1", idx.Learnings},
		{JobComment, "user said hello", "c1", idx.Comments},
	}

	for i, tc := range cases {
		src := mk(tc.entryID+".md", tc.text)
		side := filepath.Join(dir, tc.entryID+".embedding.json")
		w.Enqueue(Job{
			Kind:        tc.kind,
			SourcePath:  src,
			SidecarPath: side,
			EntryID:     tc.entryID,
			Owner:       "alpha",
			Text:        tc.text,
		})
		if !waitFor(2*time.Second, func() bool { return tc.expected.Len() >= 1 }) {
			t.Fatalf("case %d (%v): expected index never received entry", i, tc.kind)
		}
	}
}

func TestWorker_FullBufferDropsNonBlocking(t *testing.T) {
	// Tiny buffer, no consumer. Stop immediately so the goroutine is dead and
	// the queue truly has no consumer; then Enqueue should non-block-drop.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := New(ctx, newFake(), "fake-model", freshIndexes(), 1, silentLogger())
	w.Stop(context.Background())
	for i := 0; i < 100; i++ {
		w.Enqueue(Job{Kind: JobProjectSummary, EntryID: "x", Text: "hi"})
	}
}

func TestWorker_EmptyTextNoSidecar(t *testing.T) {
	dir := t.TempDir()
	side := filepath.Join(dir, "summary.embedding.json")
	idx := freshIndexes()
	w, stop := runWorker(t, newFake(), idx)
	defer stop()

	w.Enqueue(Job{
		Kind:        JobProjectSummary,
		SidecarPath: side,
		EntryID:     "p1",
		Text:        "",
	})

	// Give the worker a moment to (not) process.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(side); !os.IsNotExist(err) {
		t.Errorf("sidecar should not exist for empty text: %v", err)
	}
	if idx.Summaries.Len() != 0 {
		t.Errorf("index should be empty for empty text, got %d", idx.Summaries.Len())
	}
}

func TestWorker_MissingSourceSkipsSidecar(t *testing.T) {
	dir := t.TempDir()
	side := filepath.Join(dir, "summary.embedding.json")
	idx := freshIndexes()
	w, stop := runWorker(t, newFake(), idx)
	defer stop()

	// SourcePath points at a nonexistent file. The worker should skip
	// rather than write a sidecar that resurrects a deleted parent dir.
	w.Enqueue(Job{
		Kind:        JobProjectSummary,
		SourcePath:  filepath.Join(dir, "deleted.md"),
		SidecarPath: side,
		EntryID:     "p1",
		Text:        "hi",
	})
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(side); !os.IsNotExist(err) {
		t.Errorf("sidecar should not exist when source missing: %v", err)
	}
}

func TestWorker_StopDrains(t *testing.T) {
	idx := freshIndexes()
	dir := t.TempDir()
	src := filepath.Join(dir, "s.md")
	_ = os.WriteFile(src, []byte("hi"), 0o644)
	side := filepath.Join(dir, "s.embedding.json")

	w := New(context.Background(), newFake(), "fake-model", idx, 16, silentLogger())

	// Buffer a job and Stop; the worker should drain it before returning.
	w.Enqueue(Job{
		Kind:        JobProjectSummary,
		SourcePath:  src,
		SidecarPath: side,
		EntryID:     "p1",
		Text:        "hi",
	})
	w.Stop(context.Background())

	if _, err := os.Stat(side); err != nil {
		t.Fatalf("expected sidecar after drain: %v", err)
	}
}

// fileExists is a Stat-error-or-success helper for the diagnostic message.
func fileExists(p string) error { _, err := os.Stat(p); return err }

// TestWorker_TwoMounts_NoCrossTalk runs two workers side-by-side with their
// own indexes and providers and confirms that a job sent to mount A's queue
// only lands in A's indexes.
func TestWorker_TwoMounts_NoCrossTalk(t *testing.T) {
	dir := t.TempDir()
	srcA := filepath.Join(dir, "a.md")
	_ = os.WriteFile(srcA, []byte("a"), 0o644)
	sideA := filepath.Join(dir, "a.embedding.json")

	idxA := freshIndexes()
	idxB := freshIndexes()
	pA := newFake()
	pB := newFake()
	wA := New(context.Background(), pA, "model-a", idxA, 16, silentLogger())
	wB := New(context.Background(), pB, "model-b", idxB, 16, silentLogger())
	defer wA.Stop(context.Background())
	defer wB.Stop(context.Background())

	wA.Enqueue(Job{
		Kind: JobTicketBody, SourcePath: srcA, SidecarPath: sideA,
		EntryID: "tA", Owner: "alpha", Text: "alpha-text",
	})
	if !waitFor(2*time.Second, func() bool {
		return idxA.Tickets.Len() == 1
	}) {
		t.Fatalf("mount A index never received entry: A=%d B=%d", idxA.Tickets.Len(), idxB.Tickets.Len())
	}
	if idxB.Tickets.Len() != 0 {
		t.Errorf("mount B leaked entries from A: B.Tickets.Len = %d", idxB.Tickets.Len())
	}
	if pB.calls != 0 {
		t.Errorf("mount B provider called %d times for A's job", pB.calls)
	}
}

// TestWorker_TwoMounts_DifferentDims verifies dims are independent.
func TestWorker_TwoMounts_DifferentDims(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) (string, string) {
		s := filepath.Join(dir, name+".md")
		_ = os.WriteFile(s, []byte("x"), 0o644)
		return s, filepath.Join(dir, name+".embedding.json")
	}
	srcA, sideA := mk("a")
	srcB, sideB := mk("b")

	pA := &fakeProvider{dim: 768}
	pB := &fakeProvider{dim: 1024}
	idxA := freshIndexes()
	idxB := freshIndexes()
	wA := New(context.Background(), pA, "ma", idxA, 16, silentLogger())
	wB := New(context.Background(), pB, "mb", idxB, 16, silentLogger())
	defer wA.Stop(context.Background())
	defer wB.Stop(context.Background())

	wA.Enqueue(Job{Kind: JobTicketBody, SourcePath: srcA, SidecarPath: sideA, EntryID: "tA", Owner: "alpha", Text: "alpha"})
	wB.Enqueue(Job{Kind: JobTicketBody, SourcePath: srcB, SidecarPath: sideB, EntryID: "tB", Owner: "beta", Text: "beta"})

	if !waitFor(2*time.Second, func() bool {
		return idxA.Tickets.Len() == 1 && idxB.Tickets.Len() == 1
	}) {
		t.Fatalf("indexes not populated: A=%d B=%d", idxA.Tickets.Len(), idxB.Tickets.Len())
	}
	scA, err := vecindex.ReadSidecar(sideA)
	if err != nil {
		t.Fatal(err)
	}
	scB, err := vecindex.ReadSidecar(sideB)
	if err != nil {
		t.Fatal(err)
	}
	if scA.Dim != 768 {
		t.Errorf("A dim = %d; want 768", scA.Dim)
	}
	if scB.Dim != 1024 {
		t.Errorf("B dim = %d; want 1024", scB.Dim)
	}
}
