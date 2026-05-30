package svc

import (
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// TestDecide_TruthTable walks the decision matrix branch-by-branch with a
// hand-rolled clock so the test isn't time-of-day fragile.
func TestDecide_TruthTable(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	oldAnchor := now.AddDate(0, 0, -200) // 200 days ago
	newAnchor := now.AddDate(0, 0, -45)  // 45 days ago
	youngAnchor := now.AddDate(0, 0, -10) // 10 days ago

	enabledPolicy := defaultArchivePolicy()
	enabledPolicy.Enabled = true

	cases := []struct {
		name        string
		ticket      *domain.Ticket
		fb          domain.FeedbackRecord
		cfg         ArchivePolicy
		wantArchive bool
	}{
		{
			name:        "policy disabled → never archive",
			ticket:      &domain.Ticket{CompletedAt: &oldAnchor},
			fb:          domain.FeedbackRecord{Retrievals: 100, Dislikes: 100},
			cfg:         defaultArchivePolicy(), // Enabled=false
			wantArchive: false,
		},
		{
			name:        "already archived → never re-archive",
			ticket:      &domain.Ticket{CompletedAt: &oldAnchor, Archived: true},
			fb:          domain.FeedbackRecord{Retrievals: 10},
			cfg:         enabledPolicy,
			wantArchive: false,
		},
		{
			name:        "age below thresholds → stay",
			ticket:      &domain.Ticket{CompletedAt: &youngAnchor},
			fb:          domain.FeedbackRecord{Retrievals: 10},
			cfg:         enabledPolicy,
			wantArchive: false,
		},
		{
			name:        "old + retrieved + no feedback → archive (aged branch)",
			ticket:      &domain.Ticket{CompletedAt: &oldAnchor},
			fb:          domain.FeedbackRecord{Retrievals: 10},
			cfg:         enabledPolicy,
			wantArchive: true,
		},
		{
			name:        "old + retrieved + many likes → stay (positive feedback overrides)",
			ticket:      &domain.Ticket{CompletedAt: &oldAnchor},
			fb:          domain.FeedbackRecord{Retrievals: 10, Likes: 20, Dislikes: 0},
			cfg:         enabledPolicy,
			wantArchive: false,
		},
		{
			name:        "old + retrieved + many dislikes → archive (aged branch, ratio over)",
			ticket:      &domain.Ticket{CompletedAt: &oldAnchor},
			fb:          domain.FeedbackRecord{Retrievals: 10, Likes: 1, Dislikes: 9},
			cfg:         enabledPolicy,
			wantArchive: true,
		},
		{
			name:        "early-archive: young+rated+disliked → archive",
			ticket:      &domain.Ticket{CompletedAt: &newAnchor},
			fb:          domain.FeedbackRecord{Likes: 0, Dislikes: 5},
			cfg:         enabledPolicy,
			wantArchive: true,
		},
		{
			name:        "early-archive: young but only 2 ratings → not enough signal",
			ticket:      &domain.Ticket{CompletedAt: &newAnchor},
			fb:          domain.FeedbackRecord{Likes: 0, Dislikes: 2},
			cfg:         enabledPolicy,
			wantArchive: false,
		},
		{
			name:        "early-archive: young+rated+liked → stay",
			ticket:      &domain.Ticket{CompletedAt: &newAnchor},
			fb:          domain.FeedbackRecord{Likes: 9, Dislikes: 1},
			cfg:         enabledPolicy,
			wantArchive: false,
		},
		{
			name:        "old + few retrievals → stay (LLMs haven't seen it yet)",
			ticket:      &domain.Ticket{CompletedAt: &oldAnchor},
			fb:          domain.FeedbackRecord{Retrievals: 1},
			cfg:         enabledPolicy,
			wantArchive: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(c.ticket, c.fb, c.cfg, now)
			if got.Archive != c.wantArchive {
				t.Errorf("Decide.Archive = %v, want %v (reason=%q)", got.Archive, c.wantArchive, got.Reason)
			}
		})
	}
}

func TestResolveArchivePolicy(t *testing.T) {
	defaults := defaultArchivePolicy()
	if got := resolveArchivePolicy(nil); got != defaults {
		t.Errorf("nil record = %+v, want defaults %+v", got, defaults)
	}
	on := true
	custom := resolveArchivePolicy(&store.ArchiveConfigRecord{Enabled: &on})
	if !custom.Enabled {
		t.Errorf("Enabled=true override didn't land: %+v", custom)
	}
	if custom.MinAgeDays != defaults.MinAgeDays {
		t.Errorf("MinAgeDays drifted: %d vs %d", custom.MinAgeDays, defaults.MinAgeDays)
	}
}

// TestArchiveAndUnarchive_RoundTrip exercises the svc methods end-to-end:
// archive, list_tickets default-excludes, include_archived brings it back,
// get_ticket sees archived unconditionally, unarchive restores it.
func TestArchiveAndUnarchive_RoundTrip(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "doomed", Body: "doomed body",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Archive.
	got, err := s.ArchiveTicket(ctx, tk.ID, "stale work, retiring")
	if err != nil {
		t.Fatalf("ArchiveTicket: %v", err)
	}
	if !got.Archived || got.ArchivedAt == nil {
		t.Errorf("post-archive ticket = %+v", got)
	}

	// Default list excludes.
	listed, _, err := s.ListTickets(ctx, domain.ListTicketsInput{ProjectIDOrSlug: "alpha"})
	if err != nil {
		t.Fatalf("ListTickets: %v", err)
	}
	for _, x := range listed {
		if x.ID == tk.ID {
			t.Errorf("default ListTickets returned archived ticket")
		}
	}

	// IncludeArchived brings it back.
	listed, _, err = s.ListTickets(ctx, domain.ListTicketsInput{ProjectIDOrSlug: "alpha", IncludeArchived: true})
	if err != nil {
		t.Fatalf("ListTickets include_archived: %v", err)
	}
	found := false
	for _, x := range listed {
		if x.ID == tk.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("IncludeArchived didn't surface the archived ticket")
	}

	// get_ticket sees it unconditionally.
	direct, err := s.GetTicket(ctx, tk.ID)
	if err != nil {
		t.Fatalf("GetTicket on archived: %v", err)
	}
	if direct.ID != tk.ID || !direct.Archived {
		t.Errorf("GetTicket = %+v, want archived id=%s", direct, tk.ID)
	}

	// Second archive call rejects.
	if _, err := s.ArchiveTicket(ctx, tk.ID, "double"); err == nil {
		t.Error("double-archive should reject")
	}

	// Unarchive round-trip.
	if _, err := s.UnarchiveTicket(ctx, tk.ID, "back on track"); err != nil {
		t.Fatalf("UnarchiveTicket: %v", err)
	}
	listed, _, err = s.ListTickets(ctx, domain.ListTicketsInput{ProjectIDOrSlug: "alpha"})
	if err != nil {
		t.Fatalf("ListTickets post-unarchive: %v", err)
	}
	found = false
	for _, x := range listed {
		if x.ID == tk.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("Unarchive didn't restore default visibility")
	}
}

// TestSearchTickets_IncludeArchivedFilter: archived tickets are excluded by
// default; flipping the flag brings them back.
func TestSearchTickets_IncludeArchivedFilter(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "archive-search-test", Body: "body for archived search test",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForIdxLen(t, s.testTicketLen, 1, 5*time.Second)
	if _, err := s.ArchiveTicket(ctx, tk.ID, "shelved"); err != nil {
		t.Fatalf("ArchiveTicket: %v", err)
	}

	hits, err := s.SearchTickets(ctx, domain.SearchTicketsInput{
		Query:           "archive-search-test\n\nbody for archived search test",
		ProjectIDOrSlug: "alpha",
	})
	if err != nil {
		t.Fatalf("default search: %v", err)
	}
	for _, h := range hits {
		if h.Ticket.ID == tk.ID {
			t.Errorf("default search returned archived ticket")
		}
	}

	hits, err = s.SearchTickets(ctx, domain.SearchTicketsInput{
		Query:           "archive-search-test\n\nbody for archived search test",
		ProjectIDOrSlug: "alpha",
		IncludeArchived: true,
	})
	if err != nil {
		t.Fatalf("include_archived search: %v", err)
	}
	found := false
	for _, h := range hits {
		if h.Ticket.ID == tk.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("include_archived=true didn't surface the archived ticket")
	}
}

