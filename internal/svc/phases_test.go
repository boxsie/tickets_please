package svc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tickets_please/internal/domain"
)

// validPhaseSummary returns a 200+ char phase summary string suitable for
// CreatePhase / UpdatePhase happy-path tests.
func validPhaseSummary() string {
	return strings.Repeat("This phase scopes capability X with constraints Y and Z. ", 6)
}

func TestCreatePhase_Happy(t *testing.T) {
	s, ctx, agent, slug := freshServiceWithProject(t)

	ph, err := s.CreatePhase(ctx, slug, "Discovery work", "kickoff", validPhaseSummary())
	if err != nil {
		t.Fatalf("CreatePhase: %v", err)
	}
	if ph.Slug != "discovery-work" {
		t.Fatalf("unexpected slug: %q", ph.Slug)
	}
	if ph.Number != 1 {
		t.Fatalf("expected number 1, got %d", ph.Number)
	}
	if ph.CreatedBy == nil || ph.CreatedBy.ID != agent.ID {
		t.Fatalf("expected created_by attribution, got %+v", ph.CreatedBy)
	}

	// Files exist on disk under the expected NNN-slug dir.
	dir := filepath.Join(s.Store.Root, "projects", slug, "phases", "001-discovery-work")
	for _, f := range []string{"phase.yaml", "summary.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}

	// Initial counts are zero.
	if ph.TicketCount != 0 || ph.ActiveTicketCount != 0 {
		t.Fatalf("expected zero counts on a fresh phase, got %+v", ph)
	}
}

func TestCreatePhase_RejectsShortSummary(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	_, err := s.CreatePhase(ctx, slug, "Discovery", "", "too short")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestCreatePhase_RejectsEmptyName(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	_, err := s.CreatePhase(ctx, slug, "  ", "", validPhaseSummary())
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestCreatePhase_DuplicateSlugRejected(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	if _, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary()); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestListPhases_OrderedByNumberWithCounts(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)

	if _, err := s.CreatePhase(ctx, slug, "First phase", "", validPhaseSummary()); err != nil {
		t.Fatal(err)
	}
	p2, err := s.CreatePhase(ctx, slug, "Second phase", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}

	// Add a ticket inside the second phase.
	pSlug2 := p2.Slug
	if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "in second", PhaseIDOrSlug: &pSlug2,
	}); err != nil {
		t.Fatal(err)
	}

	out, err := s.ListPhases(ctx, slug)
	if err != nil {
		t.Fatalf("ListPhases: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 phases, got %d", len(out))
	}
	if out[0].Number != 1 || out[1].Number != 2 {
		t.Fatalf("ordering wrong: %d, %d", out[0].Number, out[1].Number)
	}
	if out[0].TicketCount != 0 {
		t.Fatalf("phase 1 should have 0 tickets, got %d", out[0].TicketCount)
	}
	if out[1].TicketCount != 1 || out[1].ActiveTicketCount != 1 {
		t.Fatalf("phase 2 counts off: total=%d active=%d", out[1].TicketCount, out[1].ActiveTicketCount)
	}
}

func TestGetPhase_BySlugAndID(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	created, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}

	bySlug, err := s.GetPhase(ctx, slug, created.Slug)
	if err != nil {
		t.Fatalf("GetPhase by slug: %v", err)
	}
	if bySlug.ID != created.ID {
		t.Fatalf("slug lookup mismatch: %s vs %s", bySlug.ID, created.ID)
	}

	byID, err := s.GetPhase(ctx, slug, created.ID)
	if err != nil {
		t.Fatalf("GetPhase by id: %v", err)
	}
	if byID.Slug != created.Slug {
		t.Fatal("id lookup mismatch")
	}

	// Summary lazily loaded from disk.
	if !strings.Contains(byID.Summary, "This phase scopes capability") {
		t.Fatalf("summary not hydrated: %q", byID.Summary)
	}
}

func TestGetPhase_NotFound(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	_, err := s.GetPhase(ctx, slug, "nope")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdatePhase_NameAndSummary(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	ph, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}

	newName := "Discovery v2"
	newSummary := strings.Repeat("Refined scope; doing X with constraints Y and Z. ", 5)
	updated, err := s.UpdatePhase(ctx, slug, ph.Slug, domain.UpdatePhaseInput{Name: &newName, Summary: &newSummary})
	if err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	if updated.Name != newName {
		t.Fatalf("name not updated: %q", updated.Name)
	}
	if updated.Summary != newSummary {
		t.Fatalf("summary not updated")
	}

	// Disk has the new summary.
	dir := filepath.Join(s.Store.Root, "projects", slug, "phases", "001-discovery", "summary.md")
	disk, err := os.ReadFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(disk), newSummary) {
		t.Fatalf("disk summary not picked up")
	}
}

func TestUpdatePhase_RejectsShortSummary(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	ph, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}
	bad := "too short"
	_, err = s.UpdatePhase(ctx, slug, ph.Slug, domain.UpdatePhaseInput{Summary: &bad})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestDeletePhase_RefusesAssignedTickets(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	ph, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}
	pSlug := ph.Slug
	if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "stuck", PhaseIDOrSlug: &pSlug,
	}); err != nil {
		t.Fatal(err)
	}
	err = s.DeletePhase(ctx, slug, ph.Slug)
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition, got %v", err)
	}
}

func TestDeletePhase_HappyPath(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	ph, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Store.Root, "projects", slug, "phases", "001-discovery")
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePhase(ctx, slug, ph.Slug); err != nil {
		t.Fatalf("DeletePhase: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("phase dir still present: %v", err)
	}

	// Cache no longer holds it.
	out, err := s.ListPhases(ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty phases list after delete, got %d", len(out))
	}
}

func TestCreatePhase_RequiresSession(t *testing.T) {
	s, _, _, slug := freshServiceWithProject(t)
	_, err := s.CreatePhase(context.Background(), slug, "Discovery", "", validPhaseSummary())
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}
