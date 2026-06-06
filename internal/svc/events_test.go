package svc

import (
	"sync"
	"testing"

	"tickets_please/internal/domain"
	"tickets_please/internal/eventbus"
)

// recordingPublisher captures every published event for assertions. Safe for
// concurrent use — the debounced agent-seen touch publishes from the session
// middleware path.
type recordingPublisher struct {
	mu     sync.Mutex
	events []eventbus.Event
}

func (p *recordingPublisher) Publish(ev eventbus.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
}

func (p *recordingPublisher) byKind(k eventbus.Kind) []eventbus.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []eventbus.Event
	for _, e := range p.events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// hasTopic reports whether topics contains want.
func hasTopic(topics []string, want string) bool {
	for _, t := range topics {
		if t == want {
			return true
		}
	}
	return false
}

func TestPublish_MutationPaths(t *testing.T) {
	s, ctx, agent, slug := freshServiceWithProject(t)
	rec := &recordingPublisher{}
	s.SetPublisher(rec)

	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "Realtime ticket"})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "starting"); err != nil {
		t.Fatalf("MoveTicket: %v", err)
	}
	if _, err := s.CreateComment(ctx, tk.ID, "a comment"); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if _, err := s.ArchiveTicket(ctx, tk.ID, "archiving"); err != nil {
		t.Fatalf("ArchiveTicket: %v", err)
	}
	if _, err := s.UnarchiveTicket(ctx, tk.ID, "back"); err != nil {
		t.Fatalf("UnarchiveTicket: %v", err)
	}
	if _, err := s.CompleteTicket(ctx, tk.ID, "tested", "did work", "learned something useful"); err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}

	// TicketMoved: on ticket + project topics, with from/to columns + actor.
	moved := rec.byKind(eventbus.KindTicketMoved)
	if len(moved) != 1 {
		t.Fatalf("TicketMoved count = %d, want 1", len(moved))
	}
	mv := moved[0]
	if mv.TicketID != tk.ID || mv.FromColumn != string(domain.ColumnTodo) || mv.ToColumn != string(domain.ColumnInProgress) {
		t.Errorf("TicketMoved payload wrong: %+v", mv)
	}
	if mv.ByAgentID != agent.ID {
		t.Errorf("TicketMoved missing actor: %+v", mv)
	}
	if !hasTopic(mv.Topics, eventbus.TopicTicket(tk.ID)) || !hasTopic(mv.Topics, eventbus.TopicProject(tk.ProjectID)) {
		t.Errorf("TicketMoved topics = %v", mv.Topics)
	}
	// Seq is stamped by the real Bus on Publish, not by svc — the recording
	// fake here sees Seq=0. The seq-stamping contract is covered in the
	// eventbus + web SSE transport tests.

	if c := rec.byKind(eventbus.KindCommentAdded); len(c) != 1 || c[0].TicketID != tk.ID || !hasTopic(c[0].Topics, eventbus.TopicTicket(tk.ID)) {
		t.Errorf("CommentAdded events = %+v", c)
	}
	if a := rec.byKind(eventbus.KindTicketArchived); len(a) != 1 {
		t.Errorf("TicketArchived count = %d, want 1", len(a))
	}
	if u := rec.byKind(eventbus.KindTicketUnarchived); len(u) != 1 {
		t.Errorf("TicketUnarchived count = %d, want 1", len(u))
	}
	if comp := rec.byKind(eventbus.KindTicketCompleted); len(comp) != 1 || comp[0].ToColumn != string(domain.ColumnDone) {
		t.Errorf("TicketCompleted events = %+v", comp)
	}
}

func TestPublish_RegisterAgentEmitsRegistered(t *testing.T) {
	s := freshService(t)
	rec := &recordingPublisher{}
	s.SetPublisher(rec)

	id, _, err := s.RegisterAgent(t.Context(), "claude:evt", "Claude Evt", nil, 0, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	reg := rec.byKind(eventbus.KindAgentRegistered)
	if len(reg) != 1 {
		t.Fatalf("AgentRegistered count = %d, want 1", len(reg))
	}
	if reg[0].AgentID != id || reg[0].AgentName != "Claude Evt" {
		t.Errorf("AgentRegistered payload = %+v", reg[0])
	}
	if !hasTopic(reg[0].Topics, eventbus.TopicGlobalAgents) {
		t.Errorf("AgentRegistered topics = %v, want global:agents", reg[0].Topics)
	}
}

func TestPublish_FireAndForgetWithNilPublisher(t *testing.T) {
	// The Nop default means a service that never had SetPublisher called still
	// mutates without panicking.
	s, ctx, _, slug := freshServiceWithProject(t)
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "no publisher"})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "go"); err != nil {
		t.Fatalf("MoveTicket without publisher panicked or errored: %v", err)
	}
}
