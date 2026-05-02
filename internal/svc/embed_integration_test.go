package svc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// waitForFile polls until path is a regular file or timeout elapses.
func waitForFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func TestEmbed_CreateProject_WritesSummarySidecarAndIndex(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	p, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary())
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	side := filepath.Join(s.Store.Root, "projects", "alpha", "summary.embedding.json")
	if !waitForFile(side, 5*time.Second) {
		t.Fatalf("summary sidecar never written")
	}

	// Resident summary index contains the project.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.SummaryIdx.Len() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := s.SummaryIdx.Snapshot()
	found := false
	for _, e := range snap {
		if e.ID == p.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SummaryIdx missing project entry; len=%d", s.SummaryIdx.Len())
	}
}

func TestEmbed_CreateTicket_WritesBodySidecar(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "do thing", Body: "details",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	// The ticket sits under projects/alpha/tickets/<NNN>-<slug>/. We don't
	// know the dir name without walking; the sidecar lives next to body.md.
	dirEntries, _ := os.ReadDir(filepath.Join(s.Store.Root, "projects", "alpha", "tickets"))
	if len(dirEntries) == 0 {
		t.Fatal("ticket dir missing")
	}
	tdir := filepath.Join(s.Store.Root, "projects", "alpha", "tickets", dirEntries[0].Name())
	side := filepath.Join(tdir, "body.embedding.json")
	if !waitForFile(side, 5*time.Second) {
		t.Fatalf("body sidecar never written next to %s", filepath.Join(tdir, "body.md"))
	}
	// TicketsIdx receives the entry.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.TicketsIdx.Len() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.TicketsIdx.Len() == 0 {
		t.Errorf("TicketsIdx empty after CreateTicket; expected entry for %s", tk.ID)
	}
}

func TestEmbed_UpdateTicket_RegeneratesBodySidecar(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "old title", Body: "old body",
	})
	if err != nil {
		t.Fatal(err)
	}
	dirEntries, _ := os.ReadDir(filepath.Join(s.Store.Root, "projects", "alpha", "tickets"))
	tdir := filepath.Join(s.Store.Root, "projects", "alpha", "tickets", dirEntries[0].Name())
	side := filepath.Join(tdir, "body.embedding.json")
	if !waitForFile(side, 5*time.Second) {
		t.Fatal("initial body sidecar missing")
	}
	first, err := os.ReadFile(side)
	if err != nil {
		t.Fatal(err)
	}

	newTitle := "new title with very different words about apples"
	if _, err := s.UpdateTicket(ctx, tk.ID, domain.UpdateTicketInput{Title: &newTitle}); err != nil {
		t.Fatal(err)
	}
	// Wait until contents change (bytes differ).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		curr, _ := os.ReadFile(side)
		if string(curr) != string(first) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("body sidecar never regenerated after title change")
}

func TestEmbed_CompleteTicket_WritesLearningsSidecar(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "do thing", Body: "details",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTicket(ctx, tk.ID,
		"tested everything", "did the work", "learned a lot about the system"); err != nil {
		t.Fatal(err)
	}

	dirEntries, _ := os.ReadDir(filepath.Join(s.Store.Root, "projects", "alpha", "tickets"))
	tdir := filepath.Join(s.Store.Root, "projects", "alpha", "tickets", dirEntries[0].Name())
	side := filepath.Join(tdir, "learnings.embedding.json")
	if !waitForFile(side, 5*time.Second) {
		t.Fatalf("learnings sidecar missing")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.LearningsIdx.Len() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.LearningsIdx.Len() == 0 {
		t.Error("LearningsIdx empty after CompleteTicket")
	}
}

func TestEmbed_CreateComment_WritesSidecar(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "do thing", Body: "details",
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateComment(ctx, tk.ID, "this is my comment")
	if err != nil {
		t.Fatal(err)
	}

	dirEntries, _ := os.ReadDir(filepath.Join(s.Store.Root, "projects", "alpha", "tickets"))
	tdir := filepath.Join(s.Store.Root, "projects", "alpha", "tickets", dirEntries[0].Name())
	commentsDir := filepath.Join(tdir, "comments")
	deadline := time.Now().Add(5 * time.Second)
	var found string
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(commentsDir)
		for _, e := range entries {
			n := e.Name()
			if strings.HasSuffix(n, ".embedding.json") {
				found = filepath.Join(commentsDir, n)
				break
			}
		}
		if found != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if found == "" {
		t.Fatalf("comment sidecar never appeared next to %s", commentsDir)
	}
	if s.CommentsIdx.Len() == 0 {
		t.Errorf("CommentsIdx empty after CreateComment(%s)", c.ID)
	}
}

func TestEmbed_BackfillRecreatesAfterDeletingAll(t *testing.T) {
	cfg := config.Config{}
	s := freshServiceWithCfg(t, cfg)
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	side := filepath.Join(s.Store.Root, "projects", "alpha", "summary.embedding.json")
	if !waitForFile(side, 5*time.Second) {
		t.Fatal("first sidecar missing")
	}
	// Tear down the service cleanly so the worker isn't running while we
	// nuke files.
	s.Close()

	// Simulate `find -name '*.embedding.json' -delete`.
	if err := os.Remove(side); err != nil {
		t.Fatal(err)
	}

	// New service against the same data dir; backfill recreates the sidecar.
	cfg.DataDir = s.Store.Root
	s2 := freshServiceWithCfg(t, cfg)
	if !waitForFile(side, 5*time.Second) {
		t.Fatalf("backfill did not regenerate sidecar")
	}
	_ = s2 // Cleanup runs via t.Cleanup.
}
