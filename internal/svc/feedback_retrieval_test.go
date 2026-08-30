package svc

import (
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// TestSearchTickets_RecordsRetrievals confirms a search call bumps the
// retrievals counter for every ticket entry returned in the result set.
func TestSearchTickets_RecordsRetrievals(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "retrieval-test-banana", Body: "details about the banana ticket",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForIdxLen(t, s.testTicketLen, 1, 5*time.Second)

	hits, err := s.SearchTickets(ctx, domain.SearchTicketsInput{
		Query:           "retrieval-test-banana details about the banana ticket",
		ProjectIDOrSlug: "alpha",
	})
	if err != nil {
		t.Fatalf("SearchTickets: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits returned")
	}
	if hits[0].EntryKey != domain.TicketEntryKey(tk.ID) {
		t.Errorf("hit[0].EntryKey = %q, want %q", hits[0].EntryKey, domain.TicketEntryKey(tk.ID))
	}

	mount := s.mountForSlug("alpha")
	if mount == nil || mount.Feedback == nil {
		t.Fatal("mount or feedback missing")
	}
	rec, ok := mount.Feedback.Get(domain.TicketEntryKey(tk.ID))
	if !ok {
		t.Fatal("no feedback record after search")
	}
	if rec.Retrievals != 1 {
		t.Errorf("Retrievals after one search = %d, want 1", rec.Retrievals)
	}
	if rec.LastUsedAt.IsZero() {
		t.Error("LastUsedAt should be set after search")
	}

	// Second search bumps to 2.
	if _, err := s.SearchTickets(ctx, domain.SearchTicketsInput{
		Query:           "retrieval-test-banana details about the banana ticket",
		ProjectIDOrSlug: "alpha",
	}); err != nil {
		t.Fatalf("second SearchTickets: %v", err)
	}
	rec, _ = mount.Feedback.Get(domain.TicketEntryKey(tk.ID))
	if rec.Retrievals != 2 {
		t.Errorf("Retrievals after two searches = %d, want 2", rec.Retrievals)
	}
}

// TestSearchTickets_EmptyResultsNoRetrievalWrite confirms that a zero-hit
// search doesn't write anything to feedback.yaml (so RecordRetrieval doesn't
// nag the disk for nothing).
func TestSearchTickets_EmptyResultsNoRetrievalWrite(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	// No tickets created → search returns empty.
	hits, err := s.SearchTickets(ctx, domain.SearchTicketsInput{
		Query: "anything at all", ProjectIDOrSlug: "alpha",
	})
	if err != nil {
		t.Fatalf("SearchTickets: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected empty hits, got %d", len(hits))
	}
	mount := s.mountForSlug("alpha")
	if mount == nil || mount.Feedback == nil {
		t.Fatal("mount or feedback missing")
	}
	// Walk should yield zero entries.
	count := 0
	_ = mount.Feedback.Walk(func(domain.EntryKey, domain.FeedbackRecord) bool { count++; return true })
	if count != 0 {
		t.Errorf("feedback store has %d entries after empty search, want 0", count)
	}
}

// TestSearchLearnings_PopulatesEntryKey confirms learning hits get the
// learning:<ticket-id> entry key.
func TestSearchLearnings_PopulatesEntryKey(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "feedback-loop-zebra", Body: "body for learning test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "starting"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnTesting, "testing now"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTicket(ctx, tk.ID, "tested it thoroughly via unit tests", "did the thing the ticket asked for", "the learning about zebras is important"); err != nil {
		t.Fatal(err)
	}
	waitForIdxLen(t, s.testLearningLen, 1, 5*time.Second)

	hits, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{
		Query:           "the learning about zebras is important",
		ProjectIDOrSlug: "alpha",
	})
	if err != nil {
		t.Fatalf("SearchLearnings: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no learning hits")
	}
	wantKey := domain.LearningEntryKey(tk.ID)
	if hits[0].EntryKey != wantKey {
		t.Errorf("hit[0].EntryKey = %q, want %q", hits[0].EntryKey, wantKey)
	}
	mount := s.mountForSlug("alpha")
	rec, ok := mount.Feedback.Get(wantKey)
	if !ok || rec.Retrievals != 1 {
		t.Errorf("learning retrieval not recorded: rec=%+v ok=%v", rec, ok)
	}
}
