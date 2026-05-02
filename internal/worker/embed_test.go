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
	w := New(p, idx, 16, silentLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, func() {
		cancel()
		w.Wait()
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

	vec, err := vecindex.ReadSidecar(side)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if len(vec) != 768 {
		t.Errorf("sidecar dim = %d, want 768", len(vec))
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
	// Tiny buffer, no consumer. Enqueue more than capacity; never blocks.
	w := New(newFake(), freshIndexes(), 1, silentLogger())
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

func TestWorker_ContextCancelDrains(t *testing.T) {
	idx := freshIndexes()
	dir := t.TempDir()
	src := filepath.Join(dir, "s.md")
	_ = os.WriteFile(src, []byte("hi"), 0o644)
	side := filepath.Join(dir, "s.embedding.json")

	w := New(newFake(), idx, 16, silentLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)

	// Buffer a job and cancel; Run should drain it before exiting.
	w.Enqueue(Job{
		Kind:        JobProjectSummary,
		SourcePath:  src,
		SidecarPath: side,
		EntryID:     "p1",
		Text:        "hi",
	})
	cancel()
	w.Wait()

	if _, err := os.Stat(side); err != nil {
		t.Fatalf("expected sidecar after drain: %v", err)
	}
}

// fileExists is a Stat-error-or-success helper for the diagnostic message.
func fileExists(p string) error { _, err := os.Stat(p); return err }
