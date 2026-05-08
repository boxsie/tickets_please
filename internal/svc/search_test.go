package svc

import (
	"errors"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// waitForIdxLen polls until idx.Len() >= n or timeout elapses. Mirrors the
// embed-integration tests' style — the worker is async so search results
// don't appear immediately after a Create*.
func waitForIdxLen(t *testing.T, getLen func() int, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if getLen() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// distinctSummary returns a project summary that's both ≥200 chars and unique
// per call so the resident SummaryIdx has cleanly distinguishable embeddings.
func distinctSummary(seed string) string {
	return seed + ": " +
		"This project has the following goals and constraints. " +
		"It targets a specific domain and follows a clear contract. " +
		"Module boundaries are described, integration points are listed, " +
		"and the operating environment is documented. " +
		"Additional details follow to round out the summary length."
}

func TestSearch_EmptyQuery_ReturnsInvalidArgument(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.SearchProjects(ctx, "   ", 0); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("SearchProjects empty query: got %v, want ErrInvalidArgument", err)
	}
	if _, err := s.SearchTickets(ctx, domain.SearchTicketsInput{Query: "", ProjectIDOrSlug: "alpha"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("SearchTickets empty query: got %v, want ErrInvalidArgument", err)
	}
	if _, err := s.SearchComments(ctx, domain.SearchCommentsInput{Query: ""}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("SearchComments empty query: got %v, want ErrInvalidArgument", err)
	}
	if _, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{Query: ""}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("SearchLearnings empty query: got %v, want ErrInvalidArgument", err)
	}
}

func TestSearchTickets_RequiresProjectFilter(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	_, err := s.SearchTickets(ctx, domain.SearchTicketsInput{Query: "anything"})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument; got %v", err)
	}
}

// TestSearch_EmptyIndex returns empty list (not error) when there are no
// embeddings yet.
func TestSearch_EmptyIndex_ReturnsEmpty(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	hits, err := s.SearchProjects(ctx, "anything plausible", 0)
	if err != nil {
		t.Fatalf("SearchProjects: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("want empty hits; got %d", len(hits))
	}

	lhits, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{Query: "x"})
	if err != nil {
		t.Fatalf("SearchLearnings: %v", err)
	}
	if len(lhits) != 0 {
		t.Errorf("want empty learning hits; got %d", len(lhits))
	}
}

// TestSearchProjects_TopHitMatchesAndFiltersPhases creates three projects
// (each with a distinctive summary) plus a phase under one of them. Searches
// using one project's exact summary text should return that project as the
// top hit, and never a phase entry — even though phases share the SummaryIdx.
func TestSearchProjects_TopHitMatchesAndFiltersPhases(t *testing.T) {
	t.Skip("multi-project scenario; re-enable once the multi-Store registry ticket lands")
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	// Three projects with distinctive summaries.
	pAlpha, err := s.CreateProject(ctx, "alpha", "Alpha", "", distinctSummary("alpha-keyword-banana"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, "beta", "Beta", "", distinctSummary("beta-keyword-zucchini")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, "gamma", "Gamma", "", distinctSummary("gamma-keyword-platypus")); err != nil {
		t.Fatal(err)
	}

	// Create a phase under alpha; it lands in the SAME SummaryIdx with a
	// distinctive summary. SearchProjects must filter it out.
	phaseSummary := distinctSummary("phase-keyword-titanium")
	if _, err := s.CreatePhase(ctx, "alpha", "Phase One", "", phaseSummary); err != nil {
		t.Fatal(err)
	}

	// Wait for at least 4 entries (3 projects + 1 phase).
	waitForIdxLen(t, s.testSummaryLen, 4, 5*time.Second)
	if got := s.testSummaryLen(); got < 4 {
		t.Fatalf("SummaryIdx didn't fill (got %d, want >=4)", got)
	}

	// Search using alpha's exact text → fakeEmbed produces an identical
	// vector → cosine ~1.0.
	hits, err := s.SearchProjects(ctx, distinctSummary("alpha-keyword-banana"), 5)
	if err != nil {
		t.Fatalf("SearchProjects: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected at least one project hit")
	}
	if hits[0].Project.ID != pAlpha.ID {
		t.Errorf("top hit = %q (slug %q); want alpha (%s)",
			hits[0].Project.Slug, hits[0].Project.Slug, pAlpha.ID)
	}
	if hits[0].Score < 0.5 {
		t.Errorf("top hit score = %v; want > 0.5", hits[0].Score)
	}

	// None of the hits should be the phase id.
	for _, h := range hits {
		if h.Project == nil {
			t.Errorf("nil project in hit")
			continue
		}
	}

	// Searching by the PHASE summary must NOT return the phase as a hit
	// (only projects). The search may return zero or some project; the
	// requirement is just "no phase id leaked through".
	phaseHits, err := s.SearchProjects(ctx, phaseSummary, 5)
	if err != nil {
		t.Fatalf("SearchProjects(phase summary): %v", err)
	}
	for _, h := range phaseHits {
		// Every hit must be a known project — by construction (only 3
		// projects exist), the slug must be one of the three.
		switch h.Project.Slug {
		case "alpha", "beta", "gamma":
			// ok
		default:
			t.Errorf("phase leaked into project hits: %+v", h)
		}
	}
}

func TestSearchTickets_ScopedToProject(t *testing.T) {
	t.Skip("multi-project scenario; re-enable once the multi-Store registry ticket lands")
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, "beta", "Beta", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tAlpha, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "alpha-ticket-banana", Body: "do something specific in alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "beta", Title: "beta-ticket-zucchini", Body: "do something specific in beta",
	}); err != nil {
		t.Fatal(err)
	}

	waitForIdxLen(t, s.testTicketLen, 2, 5*time.Second)
	if got := s.testTicketLen(); got < 2 {
		t.Fatalf("TicketsIdx didn't fill (got %d, want >=2)", got)
	}

	// Search alpha for beta's exact text. The query vector is most similar to
	// beta's ticket, but the alpha-scoped search must NOT return beta.
	hits, err := s.SearchTickets(ctx, domain.SearchTicketsInput{
		Query:           "beta-ticket-zucchini\n\ndo something specific in beta",
		ProjectIDOrSlug: "alpha",
	})
	if err != nil {
		t.Fatalf("SearchTickets: %v", err)
	}
	for _, h := range hits {
		if h.Ticket.ProjectID != tAlpha.ProjectID {
			t.Errorf("got ticket from non-alpha project: %+v", h.Ticket)
		}
	}

	// Search alpha for alpha's exact text — top hit is the alpha ticket.
	hits, err = s.SearchTickets(ctx, domain.SearchTicketsInput{
		Query:           "alpha-ticket-banana\n\ndo something specific in alpha",
		ProjectIDOrSlug: "alpha",
	})
	if err != nil {
		t.Fatalf("SearchTickets: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected alpha ticket hit")
	}
	if hits[0].Ticket.ID != tAlpha.ID {
		t.Errorf("top hit = %s; want %s", hits[0].Ticket.ID, tAlpha.ID)
	}
}

func TestSearchTickets_ColumnFilter(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk1, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "first-ticket-alpha", Body: "details here",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "second-ticket-alpha", Body: "more details",
	}); err != nil {
		t.Fatal(err)
	}

	// Move tk1 → in_progress.
	if _, err := s.MoveTicket(ctx, tk1.ID, domain.ColumnInProgress, "starting work"); err != nil {
		t.Fatal(err)
	}

	waitForIdxLen(t, s.testTicketLen, 2, 5*time.Second)

	// Search with column=todo only; tk1 (now in_progress) must NOT appear.
	hits, err := s.SearchTickets(ctx, domain.SearchTicketsInput{
		Query:           "first-ticket-alpha\n\ndetails here",
		ProjectIDOrSlug: "alpha",
		Columns:         []domain.Column{domain.ColumnTodo},
	})
	if err != nil {
		t.Fatalf("SearchTickets: %v", err)
	}
	for _, h := range hits {
		if h.Ticket.ID == tk1.ID {
			t.Errorf("tk1 (%s, column=%s) leaked through todo filter", tk1.ID, h.Ticket.Column)
		}
		if h.Ticket.Column != domain.ColumnTodo {
			t.Errorf("hit column = %s; want todo", h.Ticket.Column)
		}
	}
}

func TestSearchComments_TicketIDFilter(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk1, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "t1", Body: "b1",
	})
	if err != nil {
		t.Fatal(err)
	}
	tk2, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "t2", Body: "b2",
	})
	if err != nil {
		t.Fatal(err)
	}
	c1, err := s.CreateComment(ctx, tk1.ID, "comment about lemons on tk1")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.CreateComment(ctx, tk2.ID, "comment about lemons on tk2")
	if err != nil {
		t.Fatal(err)
	}

	waitForIdxLen(t, s.testCommentLen, 2, 5*time.Second)

	// Filter to tk1 only — c2 must not appear.
	hits, err := s.SearchComments(ctx, domain.SearchCommentsInput{
		Query:           "lemons",
		ProjectIDOrSlug: "alpha",
		TicketID:        tk1.ID,
	})
	if err != nil {
		t.Fatalf("SearchComments: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected at least one hit for tk1")
	}
	for _, h := range hits {
		if h.Comment.TicketID != tk1.ID {
			t.Errorf("hit on wrong ticket: comment %s, ticket %s", h.Comment.ID, h.Comment.TicketID)
		}
		if h.Comment.ID == c2.ID {
			t.Errorf("c2 leaked through tk1 filter")
		}
		if h.TicketTitle != "t1" {
			t.Errorf("TicketTitle = %q; want t1", h.TicketTitle)
		}
	}

	// Without the ticket filter, both should be reachable for the right query.
	hitsAll, err := s.SearchComments(ctx, domain.SearchCommentsInput{
		Query:           "lemons",
		ProjectIDOrSlug: "alpha",
	})
	if err != nil {
		t.Fatalf("SearchComments(all): %v", err)
	}
	gotIDs := map[string]bool{}
	for _, h := range hitsAll {
		gotIDs[h.Comment.ID] = true
	}
	if !gotIDs[c1.ID] || !gotIDs[c2.ID] {
		t.Errorf("expected both c1 and c2 in unfiltered hits; got %v", gotIDs)
	}
}

func TestSearchLearnings_TopHitIsCompletedTicket(t *testing.T) {
	t.Skip("multi-project scenario; re-enable once the multi-Store registry ticket lands")
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, "beta", "Beta", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "fix-the-thing", Body: "details about the thing",
	})
	if err != nil {
		t.Fatal(err)
	}
	tkBeta, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "beta", Title: "unrelated-beta-ticket", Body: "noise",
	})
	if err != nil {
		t.Fatal(err)
	}

	learningsText := "key insight: the watchdog timer must be reset BEFORE the I/O write, otherwise the kernel reaps the connection mid-stream"
	if _, err := s.CompleteTicket(ctx, tk.ID,
		"manually verified the timing change", "shifted the watchdog reset call up", learningsText,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTicket(ctx, tkBeta.ID,
		"unrelated testing", "unrelated work", "unrelated learnings about something completely different",
	); err != nil {
		t.Fatal(err)
	}

	waitForIdxLen(t, s.testLearningLen, 2, 5*time.Second)

	// Exact text match → fakeEmbed produces an identical vector → top hit.
	hits, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{Query: learningsText})
	if err != nil {
		t.Fatalf("SearchLearnings: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected at least one hit")
	}
	if hits[0].TicketID != tk.ID {
		t.Errorf("top hit ticket = %s; want %s", hits[0].TicketID, tk.ID)
	}
	if hits[0].Score < 0.5 {
		t.Errorf("top hit score = %v; want > 0.5", hits[0].Score)
	}
	if hits[0].Title != "fix-the-thing" {
		t.Errorf("top hit title = %q; want fix-the-thing", hits[0].Title)
	}
	if hits[0].Learnings != learningsText {
		t.Errorf("top hit learnings did not roundtrip; got %q", hits[0].Learnings)
	}
	if hits[0].CompletedAt.IsZero() {
		t.Errorf("CompletedAt zero on hit")
	}

	// Project-scoped: only alpha's learnings.
	hitsScoped, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{
		Query:           learningsText,
		ProjectIDOrSlug: "alpha",
	})
	if err != nil {
		t.Fatalf("SearchLearnings(alpha): %v", err)
	}
	for _, h := range hitsScoped {
		if h.TicketID == tkBeta.ID {
			t.Errorf("beta ticket leaked through alpha scope")
		}
	}
}

func TestSearch_LimitClamping(t *testing.T) {
	if got := clampSearchLimit(0); got != searchDefaultLimit {
		t.Errorf("clampSearchLimit(0) = %d; want %d", got, searchDefaultLimit)
	}
	if got := clampSearchLimit(-5); got != searchDefaultLimit {
		t.Errorf("clampSearchLimit(-5) = %d; want %d", got, searchDefaultLimit)
	}
	if got := clampSearchLimit(100); got != searchMaxLimit {
		t.Errorf("clampSearchLimit(100) = %d; want %d", got, searchMaxLimit)
	}
	if got := clampSearchLimit(7); got != 7 {
		t.Errorf("clampSearchLimit(7) = %d; want 7", got)
	}
}
