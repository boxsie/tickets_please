package svc

import (
	"context"
	"testing"
	"time"

	"tickets_please/internal/domain"
)

func TestListAgents_EmptyStore(t *testing.T) {
	s := freshService(t)
	agents, err := s.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("expected no agents, got %d", len(agents))
	}
}

func TestListAgents_SortedByLastSeenDesc(t *testing.T) {
	s := freshService(t)
	ctx := context.Background()

	// Three agents, then rewrite LastSeenAt to a known order. One is left
	// zero-valued to assert the "never seen sinks last" rule.
	ids := map[string]string{}
	for _, name := range []string{"old", "new", "never"} {
		id, _, err := s.RegisterAgent(ctx, "k:"+name, name, nil, 0, "")
		if err != nil {
			t.Fatalf("RegisterAgent %s: %v", name, err)
		}
		ids[name] = id
	}
	base := time.Now()
	setSeen := func(id string, seen time.Time, created time.Time) {
		rec, err := s.AgentStore.ReadAgent(id)
		if err != nil {
			t.Fatal(err)
		}
		rec.LastSeenAt = seen
		rec.CreatedAt = created
		if err := s.AgentStore.WriteAgentRecord(rec); err != nil {
			t.Fatal(err)
		}
	}
	setSeen(ids["old"], base.Add(-2*time.Hour), base.Add(-3*time.Hour))
	setSeen(ids["new"], base.Add(-1*time.Minute), base.Add(-3*time.Hour))
	setSeen(ids["never"], time.Time{}, base.Add(-30*time.Minute))

	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(agents))
	}
	gotOrder := []string{agents[0].Name, agents[1].Name, agents[2].Name}
	want := []string{"new", "old", "never"}
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Fatalf("sort order = %v, want %v", gotOrder, want)
		}
	}
}

func TestListAgents_CountsAccurate(t *testing.T) {
	s, ctx, agent, slug := freshServiceWithProject(t)

	// Two tickets created by `agent`; complete one; add a comment.
	t1, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTicket(ctx, t1.ID, "", "", "did the thing"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateComment(ctx, t1.ID, "a note"); err != nil {
		t.Fatal(err)
	}

	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	var got *domain.Agent
	for _, a := range agents {
		if a.ID == agent.ID {
			got = a
		}
	}
	if got == nil {
		t.Fatalf("agent %s missing from ListAgents", agent.ID)
	}
	if got.TicketsCreated != 2 {
		t.Errorf("TicketsCreated = %d, want 2", got.TicketsCreated)
	}
	if got.TicketsCompleted != 1 {
		t.Errorf("TicketsCompleted = %d, want 1", got.TicketsCompleted)
	}

	// Comment count: cross-check against an independent ground-truth tally so
	// the assertion stays correct regardless of how many system comments the
	// moves/completion emit.
	wantComments := countAuthoredComments(t, s, ctx, slug, agent.ID)
	if got.CommentsAuthored != wantComments {
		t.Errorf("CommentsAuthored = %d, want %d (ground-truth tally)", got.CommentsAuthored, wantComments)
	}
	if wantComments < 1 {
		t.Fatalf("expected the explicit CreateComment to register, ground truth was %d", wantComments)
	}
}

func TestAgentActivity_OrderedNewestFirstWithRegistration(t *testing.T) {
	s, ctx, agent, slug := freshServiceWithProject(t)

	t1, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateComment(ctx, t1.ID, "a note"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTicket(ctx, t1.ID, "", "", "did the thing"); err != nil {
		t.Fatal(err)
	}

	items, err := s.AgentActivity(ctx, agent.ID, 50)
	if err != nil {
		t.Fatalf("AgentActivity: %v", err)
	}
	if len(items) < 4 {
		t.Fatalf("expected at least created+comment+completed+registered, got %d", len(items))
	}

	// Newest-first: each timestamp >= the next.
	for i := 1; i < len(items); i++ {
		if items[i].At.After(items[i-1].At) {
			t.Fatalf("not newest-first at %d: %v then %v", i, items[i-1].At, items[i].At)
		}
	}

	// The registration event anchors the bottom (oldest).
	last := items[len(items)-1]
	if last.Kind != ActivityAgentRegistered {
		t.Errorf("expected last item to be agent_registered, got %s", last.Kind)
	}

	kinds := map[ActivityKind]bool{}
	for _, it := range items {
		kinds[it.Kind] = true
	}
	for _, want := range []ActivityKind{ActivityTicketCreated, ActivityTicketCompleted, ActivityCommentAdded, ActivityAgentRegistered} {
		if !kinds[want] {
			t.Errorf("missing activity kind %s", want)
		}
	}

	// Payload pointers follow the union contract.
	for _, it := range items {
		switch it.Kind {
		case ActivityCommentAdded:
			if it.Comment == nil {
				t.Error("comment_added with nil Comment")
			}
		case ActivityTicketCreated, ActivityTicketCompleted:
			if it.Ticket == nil {
				t.Errorf("%s with nil Ticket", it.Kind)
			}
		}
	}
}

func TestAgentActivity_RespectsLimitAndUnknownID(t *testing.T) {
	s, ctx, agent, slug := freshServiceWithProject(t)
	for range 5 {
		if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.AgentActivity(ctx, agent.ID, 3)
	if err != nil {
		t.Fatalf("AgentActivity: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("limit not honoured: got %d, want 3", len(items))
	}

	if _, err := s.AgentActivity(ctx, "no-such-agent", 10); err == nil {
		t.Fatal("expected error for unknown agent id")
	}
	if _, err := s.AgentActivity(ctx, "", 10); err == nil {
		t.Fatal("expected ErrInvalidArgument for empty agent id")
	}
}

// countAuthoredComments tallies comments authored by agentID across every
// ticket in the project — an independent ground truth for the ListAgents walk.
func countAuthoredComments(t *testing.T, s *Service, ctx context.Context, slug, agentID string) int {
	t.Helper()
	tickets, _, err := s.ListTickets(ctx, domain.ListTicketsInput{ProjectIDOrSlug: slug, IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tk := range tickets {
		comments, err := s.ListComments(ctx, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range comments {
			if c.Author != nil && c.Author.ID == agentID {
				n++
			}
		}
	}
	return n
}
