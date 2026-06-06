package attribution

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"

	"tickets_please/internal/domain"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	if err := c.Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

func TestChip_AuthorAndTime(t *testing.T) {
	tk := &domain.Ticket{
		ID:        "t1",
		CreatedBy: &domain.AgentRef{Name: "Claude Code"},
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}
	var sb strings.Builder
	if err := Chip(tk).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "ticket-attribution") {
		t.Error("expected the attribution chip wrapper class")
	}
	if !strings.Contains(out, "Claude Code") {
		t.Error("expected the author name")
	}
	if !strings.Contains(out, "<time ") {
		t.Error("expected a <time> element from reltime")
	}
}

func TestLabel_Fallbacks(t *testing.T) {
	cases := []struct {
		name string
		t    *domain.Ticket
		want string
	}{
		{"agent name", &domain.Ticket{CreatedBy: &domain.AgentRef{Name: "Agent A"}}, "Agent A"},
		{"acting-for user", &domain.Ticket{CreatedFor: &domain.UserRef{DisplayName: "Dan"}}, "Dan"},
		{"unknown", &domain.Ticket{}, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Label(c.t); got != c.want {
				t.Fatalf("Label = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCommentChip_AuthorAndSystemFallback(t *testing.T) {
	withAuthor := &domain.Comment{Author: &domain.AgentRef{Name: "Commenter"}, CreatedAt: time.Now()}
	if out := render(t, CommentChip(withAuthor)); !strings.Contains(out, "Commenter") || !strings.Contains(out, "<time ") {
		t.Errorf("expected author + time, got: %s", out)
	}
	system := &domain.Comment{CreatedAt: time.Now()}
	if out := render(t, CommentChip(system)); !strings.Contains(out, "system") {
		t.Errorf("expected system fallback label, got: %s", out)
	}
}
