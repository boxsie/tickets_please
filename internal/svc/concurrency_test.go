// Concurrency-focused tests for T13. These prove the per-project flock
// serializes same-slug mutations, that different projects don't block on
// each other, and that fsnotify-driven cache invalidation reloads when a
// `project.yaml` is rewritten out-of-band.
//
// All tests use t.TempDir() and the fake embed provider — no Docker, no
// network, no Ollama.
package svc

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// TestConcurrency_PerProjectLockSerializesSameSlug fires N goroutines at the
// same MoveTicket(s) on the same ticket in the same project. The per-project
// flock must serialize them so that:
//
//  1. Every call eventually returns (no deadlock).
//  2. Either all succeed or every "loser" observes the winner's post-state
//     (a FailedPrecondition because the ticket left the original column).
//  3. The ticket ends up in a single deterministic column.
//  4. Exactly one system_move comment file lands per successful Move.
//
// The sentinel: if the flock weren't there, the StageOps would interleave
// and we'd see torn writes — most likely a partially-applied move (yaml
// flipped without the comment, or vice versa). The on-disk invariants below
// would catch that.
func TestConcurrency_PerProjectLockSerializesSameSlug(t *testing.T) {
	s, ctx, _, tk := moveCompleteScenario(t, config.Config{})

	const N = 8
	var wg sync.WaitGroup
	var ok int32
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "concurrent move"); err == nil {
				atomic.AddInt32(&ok, 1)
			}
			// Failures are expected for losers: subsequent MoveTicket calls
			// see column != todo and return the appropriate error. The
			// invariant we care about is consistency of state, not how many
			// callers observed success.
		}()
	}
	wg.Wait()

	if atomic.LoadInt32(&ok) < 1 {
		t.Fatalf("no MoveTicket succeeded under contention")
	}

	// Final state: ticket is in_progress.
	got, err := s.GetTicket(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Column != domain.ColumnInProgress {
		t.Fatalf("expected final column in_progress, got %s", got.Column)
	}

	// On disk we should see exactly `ok` system_move files (one per winner).
	// If the flock had failed, we'd most likely see a torn state where the
	// yaml flipped without a comment (or vice versa); the equality below
	// catches that — disk count must match successful-call count.
	commentsDir := filepath.Join(
		s.Store.Root, "projects", "alpha", "tickets", "001-implement-feature", "comments",
	)
	systemMoves := 0
	entries, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-system_move.md") {
			systemMoves++
		}
	}
	if systemMoves != int(atomic.LoadInt32(&ok)) {
		t.Fatalf("expected %d system_move files (one per successful Move), got %d",
			ok, systemMoves)
	}

	// ListComments returns the same count (cache + disk agree).
	comments, err := s.ListComments(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	listMoves := 0
	for _, c := range comments {
		if c.Kind == domain.CommentKindSystemMove {
			listMoves++
		}
	}
	if listMoves != systemMoves {
		t.Fatalf("ListComments saw %d system_moves; disk has %d", listMoves, systemMoves)
	}
}

// TestConcurrency_DifferentProjectsDoNotBlock — A holds a long-running write
// on project foo (we hold foo's in-memory cache lock to simulate a slow op);
// goroutine B writes to project bar. B must complete promptly because the
// per-project flock and the cache's per-project locks are project-scoped,
// not global.
//
// The blocker: we acquire foo's LoadedProject.Lock and hold it for ~200ms;
// any svc method touching foo would wait, but bar has its own lock object so
// the call should return well under that window.
func TestConcurrency_DifferentProjectsDoNotBlock(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "foo", "Foo", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, "bar", "Bar", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	// Warm both caches so each has its own LoadedProject.Lock instance.
	if _, err := s.GetProject(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	fooLP, _, err := s.Cache.Get(ctx, "foo")
	if err != nil {
		t.Fatal(err)
	}

	// Hold foo's write lock in a goroutine for 200ms.
	const hold = 200 * time.Millisecond
	holderDone := make(chan struct{})
	go func() {
		fooLP.Lock.Lock()
		defer fooLP.Lock.Unlock()
		time.Sleep(hold)
		close(holderDone)
	}()

	// Give the holder a moment to actually grab the lock before we time
	// our bar write — otherwise we might race past the goroutine startup.
	time.Sleep(10 * time.Millisecond)

	// Time a write into bar. Threshold: well under the hold duration, with
	// slack for goroutine-scheduling jitter under -race.
	start := time.Now()
	if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "bar", Title: "fast",
	}); err != nil {
		t.Fatalf("CreateTicket on bar: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > hold/2 {
		t.Fatalf("bar write blocked on foo's lock: elapsed %v > %v", elapsed, hold/2)
	}

	<-holderDone
}

// TestConcurrency_FsnotifyCrossProcessInvalidation simulates a second process
// rewriting `project.yaml` out-of-band. The cache's fsnotify watcher should
// flip the project's Stale flag; the next Get reloads from disk.
//
// We compare a known-changed field (Description) before/after to confirm the
// reload actually happened — Stale alone could be true while the cache still
// served stale data; the description check rules that out.
func TestConcurrency_FsnotifyCrossProcessInvalidation(t *testing.T) {
	cfg := config.Config{FsnotifyEnabled: true}
	s := freshServiceWithCfg(t, cfg)
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "watch", "Watch", "before-edit", validSummary()); err != nil {
		t.Fatal(err)
	}

	// Warm the cache so the watcher attaches.
	before, err := s.GetProject(ctx, "watch")
	if err != nil {
		t.Fatal(err)
	}
	if before.Description != "before-edit" {
		t.Fatalf("baseline description = %q, want before-edit", before.Description)
	}

	// "Another process" rewrites project.yaml directly via the store.
	rec, err := s.Store.ReadProject("watch")
	if err != nil {
		t.Fatal(err)
	}
	rec.Description = "edited-out-of-band"
	yamlPath := filepath.Join(s.Store.Root, "projects", "watch", "project.yaml")
	if err := store.WriteYAMLAtomic(yamlPath, rec); err != nil {
		t.Fatal(err)
	}

	// The cache's watcher debounces ~50ms; allow up to 2s for it to flip
	// Stale and the next Get to surface the disk-side description.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := s.GetProject(ctx, "watch")
		if err != nil {
			t.Fatal(err)
		}
		if got.Description == "edited-out-of-band" {
			return // pass
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("cache did not pick up out-of-band edit within 2s")
}
