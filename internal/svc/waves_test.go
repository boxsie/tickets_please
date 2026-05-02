package svc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"tickets_please/internal/domain"
)

func TestListWaves_PhaseLessOnly_WhenFilterNil(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)

	// Create a phase plus tickets in mixed scopes.
	ph, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}
	pSlug := ph.Slug

	// Phase-less tickets: wave 1 x2, wave 2 x1, wave 0 x1 (4 total).
	for _, w := range []int{1, 1, 2, 0} {
		if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
			ProjectIDOrSlug: slug, Title: "phaseless", Wave: w,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Phased tickets — should be excluded from a nil-filter ListWaves.
	for _, w := range []int{1, 3} {
		if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
			ProjectIDOrSlug: slug, Title: "phased", Wave: w, PhaseIDOrSlug: &pSlug,
		}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := s.ListWaves(ctx, slug, nil)
	if err != nil {
		t.Fatalf("ListWaves: %v", err)
	}
	// Expect 3 waves: 1 (count 2), 2 (count 1), 0-last (count 1).
	if len(out) != 3 {
		t.Fatalf("expected 3 waves, got %d: %+v", len(out), out)
	}
	if out[0].Wave != 1 || out[0].TicketCount != 2 {
		t.Fatalf("first bucket wrong: %+v", out[0])
	}
	if out[1].Wave != 2 || out[1].TicketCount != 1 {
		t.Fatalf("second bucket wrong: %+v", out[1])
	}
	// Wave 0 sorts last.
	if out[2].Wave != 0 || out[2].TicketCount != 1 {
		t.Fatalf("wave 0 should sort last with count 1, got %+v", out[2])
	}
}

func TestListWaves_PhaseScoped(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	ph, err := s.CreatePhase(ctx, slug, "Discovery", "", validPhaseSummary())
	if err != nil {
		t.Fatal(err)
	}
	pSlug := ph.Slug

	for _, w := range []int{2, 2, 3, 0} {
		if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
			ProjectIDOrSlug: slug, Title: "p", Wave: w, PhaseIDOrSlug: &pSlug,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Phase-less ticket — should be ignored.
	if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "outside", Wave: 1,
	}); err != nil {
		t.Fatal(err)
	}

	filter := pSlug
	out, err := s.ListWaves(ctx, slug, &filter)
	if err != nil {
		t.Fatalf("ListWaves: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 waves in phase, got %d", len(out))
	}
	if out[0].Wave != 2 || out[0].TicketCount != 2 || out[0].ActiveTicketCount != 2 {
		t.Fatalf("wave 2 bucket wrong: %+v", out[0])
	}
	if out[1].Wave != 3 || out[1].TicketCount != 1 {
		t.Fatalf("wave 3 bucket wrong: %+v", out[1])
	}
	if out[2].Wave != 0 {
		t.Fatalf("wave 0 should sort last, got %+v", out[2])
	}
}

func TestListWaves_PhaseNotFound(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	missing := "no-such-phase"
	_, err := s.ListWaves(ctx, slug, &missing)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListTickets_WaveFilter(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	for _, w := range []int{0, 1, 2, 2} {
		if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
			ProjectIDOrSlug: slug, Title: strings.Repeat("t", 1+w), Wave: w,
		}); err != nil {
			t.Fatal(err)
		}
	}
	two := 2
	out, _, err := s.ListTickets(ctx, domain.ListTicketsInput{
		ProjectIDOrSlug: slug, Wave: &two,
	})
	if err != nil {
		t.Fatalf("ListTickets: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("Wave=2 expected 2 tickets, got %d", len(out))
	}
	for _, tk := range out {
		if tk.Wave != 2 {
			t.Fatalf("got non-2 wave ticket: %+v", tk)
		}
	}

	zero := 0
	out, _, err = s.ListTickets(ctx, domain.ListTicketsInput{
		ProjectIDOrSlug: slug, Wave: &zero,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Wave != 0 {
		t.Fatalf("Wave=0 expected 1 unassigned ticket, got %d (%+v)", len(out), out)
	}
}

func TestListWaves_NoWavesEmpty(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	out, err := s.ListWaves(ctx, slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty wave list, got %+v", out)
	}
	_ = context.Background
}
