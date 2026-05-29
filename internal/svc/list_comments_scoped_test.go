package svc

import (
	"testing"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// TestListCommentsScoped covers the headline workflow from the ticket: surface
// a second author's (the operator's) feedback across a project, excluding the
// agent's own system + user comments, in one call — plus the include-system
// and pagination behaviours.
func TestListCommentsScoped(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})

	// Agent A is "me" (the working agent); agent B stands in for the operator
	// / Web UI leaving feedback.
	ctxA, agentA := authedCtx(t, s)
	ctxB, agentB := authedCtx(t, s)

	if _, err := s.CreateProject(ctxA, "proj", "Proj", "", validSummary()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	t1, err := s.CreateTicket(ctxA, domain.CreateTicketInput{ProjectIDOrSlug: "proj", Title: "ticket one", Body: "b1"})
	if err != nil {
		t.Fatalf("CreateTicket t1: %v", err)
	}
	t2, err := s.CreateTicket(ctxA, domain.CreateTicketInput{ProjectIDOrSlug: "proj", Title: "ticket two", Body: "b2"})
	if err != nil {
		t.Fatalf("CreateTicket t2: %v", err)
	}

	// Agent A: one user comment + a column move (→ a system_move comment).
	if _, err := s.CreateComment(ctxA, t1.ID, "agent A note"); err != nil {
		t.Fatalf("A comment: %v", err)
	}
	if _, err := s.MoveTicket(ctxA, t1.ID, domain.ColumnInProgress, "starting work"); err != nil {
		t.Fatalf("MoveTicket: %v", err)
	}
	// Agent B (operator): one user comment on each ticket.
	if _, err := s.CreateComment(ctxB, t1.ID, "operator note on t1"); err != nil {
		t.Fatalf("B comment t1: %v", err)
	}
	if _, err := s.CreateComment(ctxB, t2.ID, "operator note on t2"); err != nil {
		t.Fatalf("B comment t2: %v", err)
	}

	// 1. "Everything NOT mine, no system noise" → exactly B's two user comments.
	got, next, err := s.ListCommentsScoped(ctxA, domain.ListCommentsScopedInput{
		ProjectIDOrSlug: "proj",
		ExcludeAuthorID: agentA.ID,
		ExcludeSystem:   true,
	})
	if err != nil {
		t.Fatalf("ListCommentsScoped (operator filter): %v", err)
	}
	if next != "" {
		t.Errorf("unexpected next_cursor %q", next)
	}
	if len(got) != 2 {
		t.Fatalf("operator filter: got %d comments, want 2", len(got))
	}
	for _, sc := range got {
		if sc.Comment.Author == nil || sc.Comment.Author.ID != agentB.ID {
			t.Errorf("comment author = %v, want agent B", sc.Comment.Author)
		}
		if sc.Comment.Kind != domain.CommentKindUser {
			t.Errorf("comment kind = %q, want user", sc.Comment.Kind)
		}
		if sc.TicketTitle == "" {
			t.Error("ScopedComment missing ticket_title")
		}
	}

	// 2. Include system → A's user note + the system_move + B's two = 4.
	all, _, err := s.ListCommentsScoped(ctxA, domain.ListCommentsScopedInput{
		ProjectIDOrSlug: "proj",
		ExcludeSystem:   false,
	})
	if err != nil {
		t.Fatalf("ListCommentsScoped (include system): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("include-system: got %d comments, want 4", len(all))
	}
	var sawSystemMove bool
	for _, sc := range all {
		if sc.Comment.Kind == domain.CommentKindSystemMove {
			sawSystemMove = true
		}
	}
	if !sawSystemMove {
		t.Error("expected a system_move comment when exclude_system=false")
	}

	// 3. Kind filter narrows to just the move.
	moves, _, err := s.ListCommentsScoped(ctxA, domain.ListCommentsScopedInput{
		ProjectIDOrSlug: "proj",
		ExcludeSystem:   false,
		Kinds:           []domain.CommentKind{domain.CommentKindSystemMove},
	})
	if err != nil {
		t.Fatalf("ListCommentsScoped (kind filter): %v", err)
	}
	if len(moves) != 1 || moves[0].Comment.Kind != domain.CommentKindSystemMove {
		t.Fatalf("kind filter: got %d (%v), want 1 system_move", len(moves), moves)
	}

	// 4. Single-ticket scope returns only t2's comment.
	t2only, _, err := s.ListCommentsScoped(ctxA, domain.ListCommentsScopedInput{
		ProjectIDOrSlug: "proj",
		TicketID:        t2.ID,
		ExcludeSystem:   false,
	})
	if err != nil {
		t.Fatalf("ListCommentsScoped (ticket scope): %v", err)
	}
	if len(t2only) != 1 || t2only[0].Comment.TicketID != t2.ID {
		t.Fatalf("ticket scope: got %d, want 1 on t2", len(t2only))
	}

	// 5. Pagination: walk all 4 with limit=1; expect 4 distinct ids, then dry.
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 10; i++ {
		page, nc, perr := s.ListCommentsScoped(ctxA, domain.ListCommentsScopedInput{
			ProjectIDOrSlug: "proj",
			ExcludeSystem:   false,
			Limit:           1,
			Cursor:          cursor,
		})
		if perr != nil {
			t.Fatalf("paginate: %v", perr)
		}
		if len(page) == 0 {
			break
		}
		if len(page) != 1 {
			t.Fatalf("limit=1 returned %d", len(page))
		}
		id := page[0].Comment.ID
		if seen[id] {
			t.Fatalf("pagination returned duplicate id %s", id)
		}
		seen[id] = true
		if nc == "" {
			break
		}
		cursor = nc
	}
	if len(seen) != 4 {
		t.Fatalf("pagination surfaced %d distinct comments, want 4", len(seen))
	}
}
