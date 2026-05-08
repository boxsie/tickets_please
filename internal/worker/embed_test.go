package worker

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// blockingProvider gates Embed on a release channel so tests can pin the
// worker mid-job (and thus keep the queue full long enough to exercise the
// blocking enqueue path).
type blockingProvider struct {
	release chan struct{}
	calls   int32
}

func (b *blockingProvider) Name() string                  { return "blocking" }
func (b *blockingProvider) Dim() int                      { return 768 }
func (b *blockingProvider) Probe(_ context.Context) error { return nil }
func (b *blockingProvider) Embed(ctx context.Context, _ string) ([]float32, error) {
	atomic.AddInt32(&b.calls, 1)
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return make([]float32, 768), nil
}

// TestWorker_EnqueueBlocking_BlocksUntilSlotOpens fills a 1-slot queue with a
// blocked-in-Embed worker, fires EnqueueBlocking from a goroutine, then
// releases one job and asserts the blocking enqueue returned nil.
func TestWorker_EnqueueBlocking_BlocksUntilSlotOpens(t *testing.T) {
	bp := &blockingProvider{release: make(chan struct{}, 8)}
	w := New(context.Background(), bp, "fake-model", freshIndexes(), 1, silentLogger())
	defer w.Stop(context.Background())

	// Job 1: gets dequeued, blocks in provider.Embed.
	w.Enqueue(Job{Kind: JobProjectSummary, EntryID: "j1", Text: "one"})
	// Wait for the worker to pull job 1 off the queue and enter Embed so the
	// channel has a free slot but the worker is pinned. Once that happens
	// job 2 will go straight onto the empty buffered slot.
	if !waitFor(time.Second, func() bool { return atomic.LoadInt32(&bp.calls) >= 1 }) {
		t.Fatalf("worker never started embedding job 1")
	}
	// Job 2: lands in the freed buffer slot (worker still in Embed).
	w.Enqueue(Job{Kind: JobProjectSummary, EntryID: "j2", Text: "two"})

	// Job 3: queue is full and the worker is pinned. EnqueueBlocking must block.
	enqDone := make(chan error, 1)
	go func() {
		enqDone <- w.EnqueueBlocking(context.Background(), Job{
			Kind: JobProjectSummary, EntryID: "j3", Text: "three",
		})
	}()

	// Confirm it's actually blocked.
	select {
	case err := <-enqDone:
		t.Fatalf("EnqueueBlocking returned early (err=%v); expected to block on full queue", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Release one Embed call; that pops job 1, the worker pulls job 2 off
	// the buffered slot, and EnqueueBlocking can finally land job 3.
	bp.release <- struct{}{}

	select {
	case err := <-enqDone:
		if err != nil {
			t.Fatalf("EnqueueBlocking returned err=%v; want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EnqueueBlocking never unblocked after slot opened")
	}

	// Drain the rest so Stop doesn't time out on shutdown.
	close(bp.release)
}

// TestWorker_EnqueueBlocking_CtxCancel asserts that a canceled context aborts
// the blocking enqueue with ctx.Err().
func TestWorker_EnqueueBlocking_CtxCancel(t *testing.T) {
	bp := &blockingProvider{release: make(chan struct{})}
	w := New(context.Background(), bp, "fake-model", freshIndexes(), 1, silentLogger())
	defer func() {
		close(bp.release)
		w.Stop(context.Background())
	}()

	// Pin the worker mid-Embed and saturate the buffer.
	w.Enqueue(Job{Kind: JobProjectSummary, EntryID: "j1", Text: "one"})
	if !waitFor(time.Second, func() bool { return atomic.LoadInt32(&bp.calls) >= 1 }) {
		t.Fatalf("worker never started embedding")
	}
	w.Enqueue(Job{Kind: JobProjectSummary, EntryID: "j2", Text: "two"})

	ctx, cancel := context.WithCancel(context.Background())
	enqDone := make(chan error, 1)
	go func() {
		enqDone <- w.EnqueueBlocking(ctx, Job{
			Kind: JobProjectSummary, EntryID: "j3", Text: "three",
		})
	}()
	// Make sure the goroutine is parked on the send before we cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-enqDone:
		if err == nil {
			t.Fatal("EnqueueBlocking returned nil on canceled ctx; want ctx.Err()")
		}
		if err != context.Canceled {
			t.Errorf("EnqueueBlocking err = %v; want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EnqueueBlocking didn't return after ctx cancel")
	}
}

// TestWorker_EnqueueBlocking_NoQueueFullWarning_BootBackfill exercises the
// boot-time pattern: enqueue more jobs than the buffer holds via
// EnqueueBlocking and assert the captured logger never emits the
// "queue full; dropping job" warning. Mirrors the 256-slot overflow that
// dropped 183 jobs on the smoke-test repo.
func TestWorker_EnqueueBlocking_NoQueueFullWarning_BootBackfill(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	w := New(context.Background(), newFake(), "fake-model", freshIndexes(), 4, log)
	defer w.Stop(context.Background())

	const total = 64 // > bufferSize (4); would have dropped many on Enqueue.
	ctx := context.Background()
	for i := 0; i < total; i++ {
		if err := w.EnqueueBlocking(ctx, Job{
			Kind: JobProjectSummary, EntryID: "j", Text: "text",
		}); err != nil {
			t.Fatalf("EnqueueBlocking[%d] err=%v", i, err)
		}
	}

	if got := buf.String(); strings.Contains(got, "queue full; dropping job") {
		t.Fatalf("blocking enqueue path emitted drop warning:\n%s", got)
	}
}
