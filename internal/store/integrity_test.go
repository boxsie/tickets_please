package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tickets_please/internal/domain"
)

func TestIntegrity_MissingSummaryIsFatal(t *testing.T) {
	s := freshStore(t)
	// Write a project.yaml without summary.md.
	pdir := s.projectDir("p")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := ProjectRecord{
		ID:        "p1",
		Slug:      "p",
		Name:      "P",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := WriteYAMLAtomic(filepath.Join(pdir, "project.yaml"), rec); err != nil {
		t.Fatal(err)
	}

	_, fatal, err := s.Integrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gotSummary := false
	for _, f := range fatal {
		if filepath.Base(f.Path) == "summary.md" {
			gotSummary = true
		}
	}
	if !gotSummary {
		t.Fatalf("expected fatal for missing summary, got %+v", fatal)
	}
}

func TestIntegrity_OrphanEmbeddingIsWarning(t *testing.T) {
	s := freshStore(t)
	pdir := s.projectDir("p")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := ProjectRecord{
		ID:        "p1",
		Slug:      "p",
		Name:      "P",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := WriteYAMLAtomic(filepath.Join(pdir, "project.yaml"), rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "summary.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Orphan: embedding without source.
	if err := os.WriteFile(filepath.Join(pdir, "ghost.embedding.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	warnings, fatal, err := s.Integrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fatal) != 0 {
		t.Fatalf("expected no fatal, got %+v", fatal)
	}
	got := false
	for _, w := range warnings {
		if filepath.Base(w.Path) == "ghost.embedding.json" {
			got = true
		}
	}
	if !got {
		t.Fatalf("expected orphan-embedding warning, got %+v", warnings)
	}
}

func TestIntegrity_DanglingAgentRefIsWarning(t *testing.T) {
	s := freshStore(t)
	// Build an AgentStore with no agents so "ghost-agent" is unknown.
	as, err := NewAgentStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewAgentStore: %v", err)
	}
	pdir := s.projectDir("p")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := "ghost-agent"
	rec := ProjectRecord{
		ID:               "p1",
		Slug:             "p",
		Name:             "P",
		CreatedByAgentID: &missing,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := WriteYAMLAtomic(filepath.Join(pdir, "project.yaml"), rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "summary.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	warnings, fatal, err := s.Integrity(context.Background(), as)
	if err != nil {
		t.Fatal(err)
	}
	if len(fatal) != 0 {
		t.Fatalf("expected no fatal, got %+v", fatal)
	}
	found := false
	for _, w := range warnings {
		if w.Message != "" && contains(w.Message, "ghost-agent") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected dangling-ref warning, got %+v", warnings)
	}
}

func TestWalkTickets_PhaseAndPhaseLess(t *testing.T) {
	s := freshStore(t)
	// Phase-less ticket.
	pl := filepath.Join(s.projectDir("p"), "tickets", "001-a")
	if err := os.MkdirAll(pl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteYAMLAtomic(filepath.Join(pl, "ticket.yaml"), TicketRecord{
		ID: "t1", ProjectID: "p", Number: 1, Title: "A", Column: domain.ColumnTodo,
	}); err != nil {
		t.Fatal(err)
	}
	// Phased ticket.
	ph := filepath.Join(s.projectDir("p"), "phases", "001-x", "tickets", "002-b")
	if err := os.MkdirAll(ph, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteYAMLAtomic(filepath.Join(ph, "ticket.yaml"), TicketRecord{
		ID: "t2", ProjectID: "p", Number: 2, Title: "B", Column: domain.ColumnTodo,
	}); err != nil {
		t.Fatal(err)
	}

	var ids []string
	if err := s.WalkTickets("p", func(_, phaseDir string, rec *TicketRecord) error {
		ids = append(ids, rec.ID+":"+phaseDir)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 tickets, got %v", ids)
	}
	if ids[0] != "t1:" {
		t.Errorf("first should be phase-less t1, got %q", ids[0])
	}
	if ids[1] != "t2:001-x" {
		t.Errorf("second should be phased t2 in 001-x, got %q", ids[1])
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
