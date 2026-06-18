package svc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// TestCreateTicket_DefaultKindIsWork proves the backfill-free default: a ticket
// created without a Kind reads back as `work`, and its on-disk ticket.yaml
// carries no `kind:` key at all (omitempty), so every pre-ideation record
// round-trips unchanged.
func TestCreateTicket_DefaultKindIsWork(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)

	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug,
		Title:           "A work ticket",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if tk.Kind != domain.KindWork {
		t.Fatalf("expected returned kind %q, got %q", domain.KindWork, tk.Kind)
	}

	// On-disk: no `kind:` key for a work ticket (omitempty + empty default).
	yamlPath := filepath.Join(s.Store.Root, "tickets", "001-a-work-ticket", "ticket.yaml")
	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read ticket.yaml: %v", err)
	}
	if strings.Contains(string(raw), "kind:") {
		t.Fatalf("work ticket.yaml should not write a kind key, got:\n%s", raw)
	}

	// Reload through the cache (drops the in-memory copy) and confirm the empty
	// kind normalises to work on hydration.
	got, err := s.GetTicket(ctx, tk.ID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.Kind != domain.KindWork {
		t.Fatalf("hydrated kind: want %q, got %q", domain.KindWork, got.Kind)
	}
}

// TestCreateTicket_KindIdea proves an idea persists with `kind: idea` and
// round-trips through hydration as KindIdea while staying in the todo column.
func TestCreateTicket_KindIdea(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)

	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug,
		Title:           "A spitball idea",
		Kind:            domain.KindIdea,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if tk.Kind != domain.KindIdea {
		t.Fatalf("expected returned kind %q, got %q", domain.KindIdea, tk.Kind)
	}
	if tk.Column != domain.ColumnTodo {
		t.Fatalf("ideas should land in todo, got %q", tk.Column)
	}

	yamlPath := filepath.Join(s.Store.Root, "tickets", "001-a-spitball-idea", "ticket.yaml")
	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read ticket.yaml: %v", err)
	}
	if !strings.Contains(string(raw), "kind: idea") {
		t.Fatalf("idea ticket.yaml should write `kind: idea`, got:\n%s", raw)
	}

	got, err := s.GetTicket(ctx, tk.ID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.Kind != domain.KindIdea {
		t.Fatalf("hydrated kind: want %q, got %q", domain.KindIdea, got.Kind)
	}
}

// TestListTickets_IdeaFilter: ideas are excluded from the default board and
// surface only with IncludeIdeas; work tickets are unaffected by the flag. The
// deterministic ListTickets path (no async embed) per #7e260496.
func TestListTickets_IdeaFilter(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)

	work, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "real work"})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}
	idea, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "spitball", Kind: domain.KindIdea})
	if err != nil {
		t.Fatalf("create idea: %v", err)
	}

	// Default: only the work ticket.
	listed, _, err := s.ListTickets(ctx, domain.ListTicketsInput{ProjectIDOrSlug: slug})
	if err != nil {
		t.Fatalf("ListTickets default: %v", err)
	}
	if !containsTicket(listed, work.ID) {
		t.Errorf("default list dropped the work ticket")
	}
	if containsTicket(listed, idea.ID) {
		t.Errorf("default list leaked an idea ticket")
	}

	// IncludeIdeas: both.
	listed, _, err = s.ListTickets(ctx, domain.ListTicketsInput{ProjectIDOrSlug: slug, IncludeIdeas: true})
	if err != nil {
		t.Fatalf("ListTickets include_ideas: %v", err)
	}
	if !containsTicket(listed, work.ID) || !containsTicket(listed, idea.ID) {
		t.Errorf("include_ideas should surface both work and idea, got %d tickets", len(listed))
	}
}

// TestSearchTickets_IdeaFilter mirrors the archived search-filter test: an idea
// is excluded from default semantic search, included with IncludeIdeas.
func TestSearchTickets_IdeaFilter(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	idea, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "idea-search-test", Body: "body for idea search test", Kind: domain.KindIdea,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForIdxLen(t, s.testTicketLen, 1, 5*time.Second)

	query := "idea-search-test\n\nbody for idea search test"
	hits, err := s.SearchTickets(ctx, domain.SearchTicketsInput{Query: query, ProjectIDOrSlug: "alpha"})
	if err != nil {
		t.Fatalf("default search: %v", err)
	}
	for _, h := range hits {
		if h.Ticket.ID == idea.ID {
			t.Errorf("default search returned an idea ticket")
		}
	}

	hits, err = s.SearchTickets(ctx, domain.SearchTicketsInput{Query: query, ProjectIDOrSlug: "alpha", IncludeIdeas: true})
	if err != nil {
		t.Fatalf("include_ideas search: %v", err)
	}
	found := false
	for _, h := range hits {
		if h.Ticket.ID == idea.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("include_ideas=true didn't surface the idea ticket")
	}
}

// TestCompleteTicket_RejectsIdea: an idea can't be completed; the error points
// at promote_idea.
func TestCompleteTicket_RejectsIdea(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	idea, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "idea", Kind: domain.KindIdea})
	if err != nil {
		t.Fatalf("create idea: %v", err)
	}
	_, err = s.CompleteTicket(ctx, idea.ID, "", "", "tried to complete an idea")
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition, got %v", err)
	}
	if !strings.Contains(err.Error(), "promote_idea") {
		t.Errorf("error should mention promote_idea, got: %v", err)
	}
}

// TestMoveTicket_IdeaStaysInTodo: an idea can't walk into the work columns.
func TestMoveTicket_IdeaStaysInTodo(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	idea, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "idea", Kind: domain.KindIdea})
	if err != nil {
		t.Fatalf("create idea: %v", err)
	}
	_, err = s.MoveTicket(ctx, idea.ID, domain.ColumnInProgress, "let me work on this idea")
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition moving idea to in_progress, got %v", err)
	}
	if !strings.Contains(err.Error(), "promote_idea") {
		t.Errorf("error should mention promote_idea, got: %v", err)
	}
}

// TestReadyOnly_ExcludesIdeas: ready_only never returns ideas, even when
// IncludeIdeas is set alongside it.
func TestReadyOnly_ExcludesIdeas(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	work, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "ready work"})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}
	idea, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "idea", Kind: domain.KindIdea})
	if err != nil {
		t.Fatalf("create idea: %v", err)
	}
	listed, _, err := s.ListTickets(ctx, domain.ListTicketsInput{ProjectIDOrSlug: slug, ReadyOnly: true, IncludeIdeas: true})
	if err != nil {
		t.Fatalf("ListTickets ready_only: %v", err)
	}
	if !containsTicket(listed, work.ID) {
		t.Errorf("ready_only should include the unblocked work ticket")
	}
	if containsTicket(listed, idea.ID) {
		t.Errorf("ready_only must never return ideas, even with IncludeIdeas")
	}
}

// TestDependsOn_RejectsIdeaTarget: a work ticket can't depend on an idea, at
// both create and update time.
func TestDependsOn_RejectsIdeaTarget(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	idea, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "idea", Kind: domain.KindIdea})
	if err != nil {
		t.Fatalf("create idea: %v", err)
	}

	// At create time.
	_, err = s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "work", DependsOn: []string{idea.ID}})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument depending on idea at create, got %v", err)
	}
	if !strings.Contains(err.Error(), "promote_idea") {
		t.Errorf("create error should mention promote_idea, got: %v", err)
	}

	// At update time.
	work, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "work2"})
	if err != nil {
		t.Fatalf("create work2: %v", err)
	}
	deps := []string{idea.ID}
	_, err = s.UpdateTicket(ctx, work.ID, domain.UpdateTicketInput{DependsOn: &deps})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument depending on idea at update, got %v", err)
	}
}

// TestPromoteIdea_FlipsInPlace: an idea promotes to work keeping its id +
// comments, gains a system_promote comment, and shows up in the default board.
func TestPromoteIdea_FlipsInPlace(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	idea, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "promote me", Kind: domain.KindIdea})
	if err != nil {
		t.Fatalf("create idea: %v", err)
	}
	if _, err := s.CreateComment(ctx, idea.ID, "an earlier thought on this idea"); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	promoted, err := s.PromoteIdea(ctx, idea.ID, "matured into real work", nil)
	if err != nil {
		t.Fatalf("PromoteIdea: %v", err)
	}
	if promoted.ID != idea.ID {
		t.Errorf("promotion must keep the same id: was %s, got %s", idea.ID, promoted.ID)
	}
	if promoted.Kind != domain.KindWork {
		t.Errorf("expected kind work after promote, got %q", promoted.Kind)
	}
	if promoted.Column != domain.ColumnTodo {
		t.Errorf("promotion keeps the todo column, got %q", promoted.Column)
	}

	// On disk the promoted ticket should look like a native work ticket (no kind key).
	yamlPath := filepath.Join(s.Store.Root, "tickets", "001-promote-me", "ticket.yaml")
	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read ticket.yaml: %v", err)
	}
	if strings.Contains(string(raw), "kind:") {
		t.Errorf("promoted ticket should not carry a kind key, got:\n%s", raw)
	}

	// Visible on the default board now.
	listed, _, err := s.ListTickets(ctx, domain.ListTicketsInput{ProjectIDOrSlug: slug})
	if err != nil {
		t.Fatalf("ListTickets: %v", err)
	}
	if !containsTicket(listed, idea.ID) {
		t.Errorf("promoted ticket should appear in default list_tickets")
	}

	// Comments intact: the original user comment + the system_promote audit.
	comments, err := s.ListComments(ctx, idea.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	var sawUser, sawPromote bool
	for _, c := range comments {
		switch c.Kind {
		case domain.CommentKindUser:
			if strings.Contains(c.Body, "earlier thought") {
				sawUser = true
			}
		case domain.CommentKindSystemPromote:
			sawPromote = true
		}
	}
	if !sawUser {
		t.Errorf("original user comment lost across promotion")
	}
	if !sawPromote {
		t.Errorf("missing system_promote audit comment")
	}
}

// TestPromoteIdea_RejectsWorkTicket: only ideas can be promoted.
func TestPromoteIdea_RejectsWorkTicket(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	work, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "already work"})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}
	_, err = s.PromoteIdea(ctx, work.ID, "nope", nil)
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition promoting a work ticket, got %v", err)
	}
}

// TestPromoteIdea_IntoPhase: the optional phase arg lands the promoted ticket in
// that phase.
func TestPromoteIdea_IntoPhase(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	ph, err := s.CreatePhase(ctx, slug, "Build", "", validSummary())
	if err != nil {
		t.Fatalf("CreatePhase: %v", err)
	}
	idea, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "phase me", Kind: domain.KindIdea})
	if err != nil {
		t.Fatalf("create idea: %v", err)
	}
	phaseRef := ph.ID
	promoted, err := s.PromoteIdea(ctx, idea.ID, "promote into Build", &phaseRef)
	if err != nil {
		t.Fatalf("PromoteIdea into phase: %v", err)
	}
	if promoted.Kind != domain.KindWork {
		t.Errorf("expected work, got %q", promoted.Kind)
	}
	if promoted.PhaseID == nil || *promoted.PhaseID != ph.ID {
		t.Errorf("promoted ticket should be in phase %s, got %v", ph.ID, promoted.PhaseID)
	}
}

func containsTicket(ts []*domain.Ticket, id string) bool {
	for _, tk := range ts {
		if tk.ID == id {
			return true
		}
	}
	return false
}

// TestTicketKind_Normalisation guards the two normalisation helpers the loader
// and CreateTicket rely on: OrWork (read) and Stored (persist) are inverses.
func TestTicketKind_Normalisation(t *testing.T) {
	if got := domain.TicketKind("").OrWork(); got != domain.KindWork {
		t.Fatalf("empty kind should normalise to work, got %q", got)
	}
	if got := domain.KindIdea.OrWork(); got != domain.KindIdea {
		t.Fatalf("idea kind should pass through OrWork, got %q", got)
	}
	if got := domain.KindWork.Stored(); got != "" {
		t.Fatalf("work should collapse to empty for storage, got %q", got)
	}
	if got := domain.TicketKind("").Stored(); got != "" {
		t.Fatalf("empty should stay empty for storage, got %q", got)
	}
	if got := domain.KindIdea.Stored(); got != domain.KindIdea {
		t.Fatalf("idea should persist verbatim, got %q", got)
	}
}

// TestTicketRecord_KindRoundTrip is the store-level round-trip: the Stored()
// form of each kind marshals and reads back identically; work/empty emit no key.
func TestTicketRecord_KindRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    domain.TicketKind
		wantKey bool
	}{
		{"idea persists", domain.KindIdea, true},
		{"work omits", domain.KindWork, false},
		{"empty omits", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &store.TicketRecord{ID: "x", Title: "t", Column: domain.ColumnTodo, Kind: tc.kind.Stored()}
			b, err := store.MarshalYAML(rec)
			if err != nil {
				t.Fatalf("MarshalYAML: %v", err)
			}
			hasKey := strings.Contains(string(b), "kind:")
			if hasKey != tc.wantKey {
				t.Fatalf("kind key present=%v, want %v; yaml:\n%s", hasKey, tc.wantKey, b)
			}
			path := filepath.Join(t.TempDir(), "ticket.yaml")
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			var back store.TicketRecord
			if err := store.ReadYAML(path, &back); err != nil {
				t.Fatalf("ReadYAML: %v", err)
			}
			if got := back.Kind.OrWork(); got != tc.kind.OrWork() {
				t.Fatalf("round-trip kind: want %q, got %q", tc.kind.OrWork(), got)
			}
		})
	}
}
