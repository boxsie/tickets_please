package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/store"
)

// freshStoreForBackfill builds a *store.Store rooted at t.TempDir().
func freshStoreForBackfill(t *testing.T) *store.Store {
	t.Helper()
	cfg := config.Config{
		DataDir:            t.TempDir(),
		AutoCommit:         false,
		LockTimeoutSeconds: 5,
	}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return s
}

// writeFile writes content to path, creating parent dirs.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedProject writes a minimal project.yaml + summary.md tree on disk so
// backfill has something to walk.
func seedProject(t *testing.T, st *store.Store, slug, projectID string) {
	t.Helper()
	dir := st.ProjectDir(slug)
	yaml := "" +
		"id: " + projectID + "\n" +
		"slug: " + slug + "\n" +
		"name: " + slug + "\n" +
		"created_at: 2026-01-01T00:00:00Z\n" +
		"updated_at: 2026-01-01T00:00:00Z\n"
	writeFile(t, filepath.Join(dir, "project.yaml"), yaml)
	writeFile(t, filepath.Join(dir, "summary.md"), "the project summary text")
}

// seedTicket writes a minimal ticket.yaml + body.md inside the project's
// phase-less tickets dir.
func seedTicket(t *testing.T, st *store.Store, slug, ticketID, dirName, title, body string, done bool, learnings string) string {
	t.Helper()
	tdir := filepath.Join(st.ProjectDir(slug), "tickets", dirName)
	col := "todo"
	if done {
		col = "done"
	}
	yaml := "" +
		"id: " + ticketID + "\n" +
		"project_id: pid\n" +
		"number: 1\n" +
		"title: " + title + "\n" +
		"column: " + col + "\n" +
		"created_at: 2026-01-01T00:00:00Z\n" +
		"updated_at: 2026-01-01T00:00:00Z\n"
	writeFile(t, filepath.Join(tdir, "ticket.yaml"), yaml)
	writeFile(t, filepath.Join(tdir, "body.md"), body)
	if done {
		md := "## Testing evidence\nTE\n\n## Work summary\nWS\n\n## Learnings\n" + learnings + "\n"
		writeFile(t, filepath.Join(tdir, "completion.md"), md)
	}
	return tdir
}

// seedComment writes a comment markdown with the canonical filename pattern.
// shortID must be the first 8 hex chars of the comment id (without the dash).
func seedComment(t *testing.T, ticketDir, ts, shortID, kind, body string) string {
	t.Helper()
	dir := filepath.Join(ticketDir, "comments")
	frontmatter := "---\n" +
		"id: " + shortID + "-aaaa-aaaa-aaaa-aaaaaaaaaaaa\n" +
		"ticket_id: tid\n" +
		"kind: " + kind + "\n" +
		"created_at: 2026-01-01T00:00:00Z\n" +
		"---\n" + body + "\n"
	name := ts + "-" + shortID + "-" + kind + ".md"
	path := filepath.Join(dir, name)
	writeFile(t, path, frontmatter)
	return path
}

func TestBackfill_EnqueuesProjectSummary(t *testing.T) {
	st := freshStoreForBackfill(t)
	seedProject(t, st, "alpha", "p-id-1")

	idx := freshIndexes()
	w, stop := runWorker(t, newFake(), idx)
	defer stop()

	bf := NewBackfiller(st, w, silentLogger())
	if err := bf.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	side := filepath.Join(st.ProjectDir("alpha"), "summary.embedding.json")
	if !waitFor(2*time.Second, func() bool {
		_, err := os.Stat(side)
		return err == nil && idx.Summaries.Len() == 1
	}) {
		t.Fatalf("project summary sidecar never written: idx=%d", idx.Summaries.Len())
	}
}

func TestBackfill_SkipsExistingSidecar(t *testing.T) {
	st := freshStoreForBackfill(t)
	seedProject(t, st, "alpha", "p-id-1")
	side := filepath.Join(st.ProjectDir("alpha"), "summary.embedding.json")
	writeFile(t, side, "[]") // pre-existing sidecar; backfill must skip.

	p := newFake()
	idx := freshIndexes()
	w, stop := runWorker(t, p, idx)
	defer stop()

	bf := NewBackfiller(st, w, silentLogger())
	if err := bf.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Give the worker a moment to (not) work.
	time.Sleep(50 * time.Millisecond)
	if p.calls != 0 {
		t.Errorf("provider was called %d times; expected 0 (sidecar already present)", p.calls)
	}
}

func TestBackfill_TicketBodyAndLearnings(t *testing.T) {
	st := freshStoreForBackfill(t)
	seedProject(t, st, "alpha", "p-id-1")
	tdir := seedTicket(t, st, "alpha", "t-id-1", "001-impl", "Impl",
		"do the thing", true, "the lesson")

	idx := freshIndexes()
	w, stop := runWorker(t, newFake(), idx)
	defer stop()

	bf := NewBackfiller(st, w, silentLogger())
	if err := bf.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	bodySide := filepath.Join(tdir, "body.embedding.json")
	learnSide := filepath.Join(tdir, "learnings.embedding.json")
	if !waitFor(2*time.Second, func() bool {
		_, e1 := os.Stat(bodySide)
		_, e2 := os.Stat(learnSide)
		return e1 == nil && e2 == nil &&
			idx.Tickets.Len() == 1 && idx.Learnings.Len() == 1
	}) {
		t.Fatalf("body+learnings sidecars never written: tickets=%d learnings=%d",
			idx.Tickets.Len(), idx.Learnings.Len())
	}
}

func TestBackfill_Comments(t *testing.T) {
	st := freshStoreForBackfill(t)
	seedProject(t, st, "alpha", "p-id-1")
	tdir := seedTicket(t, st, "alpha", "t-id-1", "001-impl", "Impl", "body", false, "")
	commentPath := seedComment(t, tdir, "20260101T000000.000000000Z", "deadbeef", "user", "user wrote this")

	idx := freshIndexes()
	w, stop := runWorker(t, newFake(), idx)
	defer stop()

	bf := NewBackfiller(st, w, silentLogger())
	if err := bf.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	side := filepath.Join(filepath.Dir(commentPath),
		"20260101T000000.000000000Z-deadbeef-user.embedding.json")
	if !waitFor(2*time.Second, func() bool {
		_, err := os.Stat(side)
		return err == nil && idx.Comments.Len() == 1
	}) {
		t.Fatalf("comment sidecar never written: idx=%d", idx.Comments.Len())
	}
}

func TestBackfill_ResumesAfterDelete(t *testing.T) {
	st := freshStoreForBackfill(t)
	seedProject(t, st, "alpha", "p-id-1")
	side := filepath.Join(st.ProjectDir("alpha"), "summary.embedding.json")

	// First pass: backfill writes the sidecar.
	{
		idx := freshIndexes()
		w, stop := runWorker(t, newFake(), idx)
		bf := NewBackfiller(st, w, silentLogger())
		_ = bf.Run(context.Background())
		waitFor(2*time.Second, func() bool { _, e := os.Stat(side); return e == nil })
		stop()
	}

	// Operator nukes all sidecars.
	if err := os.Remove(side); err != nil {
		t.Fatal(err)
	}

	// Second pass: backfill regenerates.
	idx := freshIndexes()
	w, stop := runWorker(t, newFake(), idx)
	defer stop()
	bf := NewBackfiller(st, w, silentLogger())
	_ = bf.Run(context.Background())
	if !waitFor(2*time.Second, func() bool {
		_, e := os.Stat(side)
		return e == nil && idx.Summaries.Len() == 1
	}) {
		t.Fatal("backfill did not regenerate after delete")
	}
}
