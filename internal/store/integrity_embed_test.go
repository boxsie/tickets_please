package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedProjectForIntegrity writes the minimum a project needs to pass the
// structural checks, so a test can add exactly one defect and see it named.
func seedProjectForIntegrity(t *testing.T, s *Store, slug string) string {
	t.Helper()
	pdir := s.projectDir(slug)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteYAMLAtomic(filepath.Join(pdir, "project.yaml"), ProjectRecord{
		ID: "p1", Slug: slug, Name: "P",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "summary.md"), []byte("summary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return pdir
}

// writeTicketAt creates a minimally-valid ticket dir at relDir under pdir.
func writeTicketAt(t *testing.T, pdir, relDir, id string, number int) string {
	t.Helper()
	dir := filepath.Join(pdir, relDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteYAMLAtomic(filepath.Join(dir, "ticket.yaml"), TicketRecord{
		ID: id, ProjectID: "p1", Number: number, Title: "t", Column: "todo",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "body.md"), []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestIntegrity_EmbedFailureMarkerIsWarning: the marker the worker leaves when
// it gives up on an entry has to be reachable by a human running `check`, or
// it's just a differently-shaped silence.
func TestIntegrity_EmbedFailureMarkerIsWarning(t *testing.T) {
	s := freshStore(t)
	pdir := seedProjectForIntegrity(t, s, "p")
	tdir := writeTicketAt(t, pdir, filepath.Join("tickets", "001-thing"), "t-1", 1)

	marker := filepath.Join(tdir, "learnings.embedding.failed")
	body := `{"entry_id":"t-1","kind":"ticket_learnings","provider":"ollama",` +
		`"error":"ollama: status 500: json: unsupported value: NaN",` +
		`"attempts":3,"failed_at":"2026-08-04T14:00:00Z"}`
	if err := os.WriteFile(marker, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	warnings, fatal, err := s.Integrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fatal) != 0 {
		t.Fatalf("expected no fatal, got %+v", fatal)
	}
	var found *Warning
	for i := range warnings {
		if filepath.Base(warnings[i].Path) == "learnings.embedding.failed" {
			found = &warnings[i]
		}
	}
	if found == nil {
		t.Fatalf("check did not report the embed-failure marker; warnings: %+v", warnings)
	}
	// The message must carry the cause — "something failed" is not actionable.
	if !strings.Contains(found.Message, "NaN") {
		t.Errorf("warning drops the recorded error: %q", found.Message)
	}
	if !strings.Contains(found.Message, "absent from search") {
		t.Errorf("warning doesn't say what the consequence is: %q", found.Message)
	}
}

// TestIntegrity_MissingSidecarIsNotAWarning pins a deliberate omission:
// sidecars are gitignored and rebuilt on load, so on a fresh clone every one
// of them is absent. Warning per missing sidecar would emit hundreds of lines
// on a healthy store and bury the markers that actually mean something.
func TestIntegrity_MissingSidecarIsNotAWarning(t *testing.T) {
	s := freshStore(t)
	pdir := seedProjectForIntegrity(t, s, "p")
	writeTicketAt(t, pdir, filepath.Join("tickets", "001-thing"), "t-1", 1)

	warnings, fatal, err := s.Integrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fatal) != 0 {
		t.Fatalf("expected no fatal, got %+v", fatal)
	}
	if len(warnings) != 0 {
		t.Errorf("a store with no sidecars at all should be quiet; got %+v", warnings)
	}
}

// TestIntegrity_DuplicateTicketPathIsFatal is the invariant ticket 151 asked
// for: a half-staged move can leave the same ticket id at two paths, and the
// store's behaviour then is undefined — which copy loads, what the next
// number allocation sees.
func TestIntegrity_DuplicateTicketPathIsFatal(t *testing.T) {
	s := freshStore(t)
	pdir := seedProjectForIntegrity(t, s, "p")
	writeTicketAt(t, pdir, filepath.Join("tickets", "001-thing"), "dup-id", 1)
	writeTicketAt(t, pdir, filepath.Join("phases", "001-alpha", "tickets", "001-thing"), "dup-id", 1)

	_, fatal, err := s.Integrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range fatal {
		if strings.Contains(f.Message, "dup-id") && strings.Contains(f.Message, "2 paths") {
			found = true
		}
	}
	if !found {
		t.Fatalf("duplicate ticket id not reported as fatal; got %+v", fatal)
	}
}
