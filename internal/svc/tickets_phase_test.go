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

func TestAssignTicketToPhase_RejectsEmptyComment(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ph, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}
	pSlug := ph.Slug
	_, err = s.AssignTicketToPhase(ctx, tk.ID, &pSlug, "")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestAssignTicketToPhase_RequiresSession(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	pslug := "discovery"
	_, err = s.AssignTicketToPhase(context.Background(), tk.ID, &pslug, "x")
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAssignTicketToPhase_PhaselessToPhase_MovesDir(t *testing.T) {
	s, ctx, agent, slug := freshServiceWithProject(t)
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "task"})
	if err != nil {
		t.Fatal(err)
	}
	ph, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}
	pSlug := ph.Slug

	updated, err := s.AssignTicketToPhase(ctx, tk.ID, &pSlug, "Need this scoped under discovery.")
	if err != nil {
		t.Fatalf("AssignTicketToPhase: %v", err)
	}
	if updated.PhaseID == nil || *updated.PhaseID != ph.ID {
		t.Fatalf("expected PhaseID=%s, got %v", ph.ID, updated.PhaseID)
	}

	// Old phase-less dir is gone.
	oldDir := filepath.Join(s.Store.Root, "projects", slug, "tickets", "001-task")
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old dir still present: %v", err)
	}
	// New phased dir exists with same NNN-slug.
	newDir := filepath.Join(s.Store.Root, "projects", slug, "phases", "001-discovery", "tickets", "001-task")
	for _, f := range []string{"ticket.yaml", "body.md"} {
		if _, err := os.Stat(filepath.Join(newDir, f)); err != nil {
			t.Fatalf("missing post-rename %s: %v", f, err)
		}
	}

	// system_move comment exists with the agent recorded as author + body
	// carries the prefix.
	comments, err := s.ListComments(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	c := comments[0]
	if c.Kind != domain.CommentKindSystemMove {
		t.Fatalf("expected system_move kind, got %s", c.Kind)
	}
	if c.Author == nil || c.Author.ID != agent.ID {
		t.Fatalf("comment author wrong: %+v", c.Author)
	}
	if !strings.HasPrefix(c.Body, "Phase reassignment: → "+ph.Name) {
		t.Fatalf("body missing prefix: %q", c.Body)
	}
	if !strings.Contains(c.Body, "Need this scoped under discovery.") {
		t.Fatalf("body missing user comment: %q", c.Body)
	}
}

func TestAssignTicketToPhase_PhaseToPhaseless_MovesBack(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	ph, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}
	pSlug := ph.Slug
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "task", PhaseIDOrSlug: &pSlug,
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := s.AssignTicketToPhase(ctx, tk.ID, nil, "Lifting this back to project level.")
	if err != nil {
		t.Fatalf("AssignTicketToPhase nil: %v", err)
	}
	if updated.PhaseID != nil {
		t.Fatalf("expected PhaseID=nil, got %v", *updated.PhaseID)
	}

	oldDir := filepath.Join(s.Store.Root, "projects", slug, "phases", "001-discovery", "tickets", "001-task")
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old phase dir still present: %v", err)
	}
	newDir := filepath.Join(s.Store.Root, "projects", slug, "tickets", "001-task")
	if _, err := os.Stat(filepath.Join(newDir, "ticket.yaml")); err != nil {
		t.Fatalf("expected ticket.yaml at %s: %v", newDir, err)
	}

	// Body of the system_move comment: target is "none" when phase-less.
	comments, err := s.ListComments(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if !strings.HasPrefix(comments[0].Body, "Phase reassignment: → none") {
		t.Fatalf("expected 'none' target, got %q", comments[0].Body)
	}
}

func TestAssignTicketToPhase_NoOp_RejectsAlreadyInTarget(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	ph, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}
	pSlug := ph.Slug
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "task", PhaseIDOrSlug: &pSlug,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AssignTicketToPhase(ctx, tk.ID, &pSlug, "noop")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestAssignTicketToPhase_ListAfterMove_ReflectsNewScope(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	ph, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}
	pSlug := ph.Slug
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssignTicketToPhase(ctx, tk.ID, &pSlug, "moving"); err != nil {
		t.Fatal(err)
	}

	// Phase filter should now find it; phase-less filter should not.
	dash := "-"
	phaseLess, _, err := s.ListTickets(ctx, domain.ListTicketsInput{
		ProjectIDOrSlug: slug, PhaseIDOrSlug: &dash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(phaseLess) != 0 {
		t.Fatalf("expected phase-less list to be empty after move, got %d", len(phaseLess))
	}

	phased, _, err := s.ListTickets(ctx, domain.ListTicketsInput{
		ProjectIDOrSlug: slug, PhaseIDOrSlug: &pSlug,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(phased) != 1 || phased[0].ID != tk.ID {
		t.Fatalf("expected the moved ticket in phase listing, got %+v", phased)
	}
}
