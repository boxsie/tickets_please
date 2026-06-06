package tickets

import (
	"context"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/domain"
)

func renderMeta(t *testing.T, p DetailProps) string {
	t.Helper()
	var sb strings.Builder
	if err := TicketMetadata(p).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render TicketMetadata: %v", err)
	}
	return sb.String()
}

func baseMetaProps() DetailProps {
	now := time.Now()
	return DetailProps{
		ProjectSlug: "demo",
		IsDone:      false,
		Ticket: &domain.Ticket{
			ID:        "abc123def4567890",
			Title:     "Sample ticket",
			Column:    domain.ColumnInProgress,
			CreatedBy: &domain.AgentRef{Name: "Claude Code"},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
}

// The created-by agent and the acting-for user render as links to their detail
// pages (/agents/{id}, /u/{id}).
func TestTicketMetadata_AttributionLinks(t *testing.T) {
	p := baseMetaProps()
	p.Ticket.CreatedBy = &domain.AgentRef{ID: "agent-99", Name: "Claude Code"}
	p.Ticket.CreatedFor = &domain.UserRef{UserID: "user-7", DisplayName: "Dan"}
	out := renderMeta(t, p)
	if !strings.Contains(out, `href="/agents/agent-99"`) {
		t.Errorf("created-by agent should link to /agents/{id}:\n%s", out)
	}
	if !strings.Contains(out, `href="/u/user-7"`) {
		t.Errorf("acting-for user should link to /u/{id}:\n%s", out)
	}
}

func TestTicketMetadata_CreatedAndEntryKeyAlwaysPresent(t *testing.T) {
	out := renderMeta(t, baseMetaProps())
	if !strings.Contains(out, "<dt>Created</dt>") {
		t.Error("expected Created row")
	}
	if !strings.Contains(out, "Claude Code") {
		t.Error("expected creating agent name")
	}
	if !strings.Contains(out, "ticket:abc123def4567890") {
		t.Error("expected entry key ticket:<id>")
	}
	if !strings.Contains(out, `data-copy="ticket:abc123def4567890"`) {
		t.Error("expected copy button wired with data-copy")
	}
	if !strings.Contains(out, "<time ") {
		t.Error("expected a <time> element from reltime.Time")
	}
}

func TestTicketMetadata_UpdatedOnlyWhenDivergedFromCreated(t *testing.T) {
	// Created == Updated → no Updated row.
	p := baseMetaProps()
	if out := renderMeta(t, p); strings.Contains(out, "<dt>Updated</dt>") {
		t.Error("did not expect Updated row when UpdatedAt == CreatedAt")
	}
	// Updated > 1m later → row appears.
	p.Ticket.UpdatedAt = p.Ticket.CreatedAt.Add(5 * time.Minute)
	if out := renderMeta(t, p); !strings.Contains(out, "<dt>Updated</dt>") {
		t.Error("expected Updated row when UpdatedAt diverges by >1m")
	}
}

func TestTicketMetadata_CompletedOnlyWhenDone(t *testing.T) {
	p := baseMetaProps()
	if out := renderMeta(t, p); strings.Contains(out, "<dt>Completed</dt>") {
		t.Error("did not expect Completed row on an open ticket")
	}
	done := p.Ticket.CreatedAt.Add(time.Hour)
	p.IsDone = true
	p.Ticket.Column = domain.ColumnDone
	p.Ticket.CompletedAt = &done
	p.Ticket.CompletedBy = &domain.AgentRef{Name: "Finisher"}
	out := renderMeta(t, p)
	if !strings.Contains(out, "<dt>Completed</dt>") {
		t.Error("expected Completed row on a done ticket")
	}
	if !strings.Contains(out, "Finisher") {
		t.Error("expected completing agent name")
	}
}

func TestTicketMetadata_ActingForOnlyWhenSet(t *testing.T) {
	p := baseMetaProps()
	if out := renderMeta(t, p); strings.Contains(out, "<dt>Acting for</dt>") {
		t.Error("did not expect Acting for row without CreatedFor/CompletedFor")
	}
	p.Ticket.CreatedFor = &domain.UserRef{UserID: "u1", DisplayName: "Dan"}
	out := renderMeta(t, p)
	if !strings.Contains(out, "<dt>Acting for</dt>") || !strings.Contains(out, "Dan") {
		t.Error("expected Acting for row naming the user")
	}
}

func TestTicketMetadata_ArchivedRowVisuallyDistinct(t *testing.T) {
	p := baseMetaProps()
	if out := renderMeta(t, p); strings.Contains(out, "meta-pair-archived") {
		t.Error("did not expect archived row on a live ticket")
	}
	at := p.Ticket.CreatedAt.Add(2 * time.Hour)
	p.Ticket.Archived = true
	p.Ticket.ArchivedAt = &at
	out := renderMeta(t, p)
	if !strings.Contains(out, "meta-pair-archived") {
		t.Error("expected the archived row to carry the distinct class")
	}
	if !strings.Contains(out, "<dt>Archived</dt>") {
		t.Error("expected Archived row")
	}
}

func TestTicketMetadata_DependsAndParallelPopovers(t *testing.T) {
	p := baseMetaProps()
	if out := renderMeta(t, p); strings.Contains(out, "<dt>Dependencies</dt>") || strings.Contains(out, "<dt>Parallelizable with</dt>") {
		t.Error("did not expect relationship rows with empty slices")
	}
	p.Depends = []*domain.Ticket{
		{ID: "dep1", Title: "Upstream A", Column: domain.ColumnDone},
		{ID: "dep2", Title: "Upstream B", Column: domain.ColumnTodo},
	}
	p.Parallel = []*domain.Ticket{
		{ID: "par1", Title: "Sibling", Column: domain.ColumnInProgress},
	}
	out := renderMeta(t, p)
	if !strings.Contains(out, "<dt>Dependencies</dt>") || !strings.Contains(out, "2 dependencies") {
		t.Error("expected Dependencies popover with plural count")
	}
	if !strings.Contains(out, "Upstream A") || !strings.Contains(out, "Upstream B") {
		t.Error("expected dependency titles in the popover list")
	}
	if !strings.Contains(out, "<dt>Parallelizable with</dt>") || !strings.Contains(out, "1 ticket") {
		t.Error("expected Parallelizable-with popover with singular count")
	}
	if !strings.Contains(out, "Sibling") {
		t.Error("expected parallelizable ticket title")
	}
}
