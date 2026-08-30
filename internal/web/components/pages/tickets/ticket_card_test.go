package tickets

import (
	"context"
	"strings"
	"testing"

	"tickets_please/internal/domain"
)

func renderCard(t *testing.T, p TicketCardProps) string {
	t.Helper()
	var sb strings.Builder
	if err := TicketCard(p).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render TicketCard: %v", err)
	}
	return sb.String()
}

// An active ticket card carries no archived treatment.
func TestTicketCard_ActiveHasNoArchivedTreatment(t *testing.T) {
	out := renderCard(t, TicketCardProps{
		ProjectSlug: "demo",
		Ticket:      &domain.Ticket{ID: "t1", Title: "Active one", Column: domain.ColumnTodo},
	})
	if strings.Contains(out, "ticket-card archived") || strings.Contains(out, ">archived<") {
		t.Errorf("active card should not be archived-styled:\n%s", out)
	}
}

// An archived ticket card gets the muted `archived` class on the <li> and the
// inline "archived" pill.
func TestTicketCard_ArchivedShowsPillAndClass(t *testing.T) {
	out := renderCard(t, TicketCardProps{
		ProjectSlug: "demo",
		Ticket:      &domain.Ticket{ID: "t2", Title: "Archived one", Column: domain.ColumnDone, Archived: true},
	})
	if !strings.Contains(out, "archived") {
		t.Errorf("archived card should carry the archived class:\n%s", out)
	}
	if !strings.Contains(out, ">archived<") {
		t.Errorf("archived card missing the archived pill:\n%s", out)
	}
}
