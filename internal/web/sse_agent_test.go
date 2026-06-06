package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/eventbus"
	"tickets_please/internal/svc"
)

// On a new registration the /agents index (subscribed to global:agents) gets a
// prepend patch carrying the new row with the one-shot highlight class.
func TestSSE_AgentRegistered_PrependsRow(t *testing.T) {
	srv, _, deps := sseTestServerDeps(t)
	s := deps.Service

	br, done := sseConnect(t, srv.URL, eventbus.TopicGlobalAgents)
	defer done()

	if _, _, err := s.RegisterAgent(context.Background(), "newbie-key", "Newbie Agent", map[string]string{"model": "opus"}, time.Hour, ""); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	lines := readUntil(t, br, "Newbie Agent")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "datastar-patch-elements") {
		t.Errorf("registration patch not an elements frame:\n%s", joined)
	}
	if !strings.Contains(joined, "mode prepend") {
		t.Errorf("registration row should prepend:\n%s", joined)
	}
	if !strings.Contains(joined, "#agents-tbody") {
		t.Errorf("registration patch missing #agents-tbody target:\n%s", joined)
	}
	if !strings.Contains(joined, "agent-row-new") {
		t.Errorf("registration row missing highlight class:\n%s", joined)
	}
}

// AgentSeen morphs just the agent's last-seen cell (shared by index + detail).
func TestSSE_AgentSeen_TicksLastSeen(t *testing.T) {
	srv, bus, deps := sseTestServerDeps(t)
	s := deps.Service

	id, _, err := s.RegisterAgent(context.Background(), "seen-key", "Seen Agent", nil, time.Hour, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	br, done := sseConnect(t, srv.URL, eventbus.TopicGlobalAgents)
	defer done()

	bus.Publish(eventbus.Event{
		Kind:       eventbus.KindAgentSeen,
		Topics:     []string{eventbus.TopicGlobalAgents, eventbus.TopicAgent(id)},
		AgentID:    id,
		AgentName:  "Seen Agent",
		LastSeenAt: time.Now(),
	})

	lines := readUntil(t, br, "agent-lastseen-"+id)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "mode inner") {
		t.Errorf("last-seen patch should be an inner morph:\n%s", joined)
	}
	// The morphed content is a relative <time> element.
	timeLines := readUntil(t, br, "<time")
	if !strings.Contains(strings.Join(timeLines, "\n"), "<time") {
		t.Errorf("last-seen patch missing a <time> element")
	}
}

// On the detail page (subscribed to agent:{id}) the agent's own ticket/comment
// activity prepends to the feed.
func TestSSE_AgentDetail_ActivityPrepends(t *testing.T) {
	srv, _, deps := sseTestServerDeps(t)
	s := deps.Service

	id, _, err := s.RegisterAgent(context.Background(), "feed-key", "Feed Agent", nil, time.Hour, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(context.Background(), id)
	if _, err := s.CreateProject(authed, "feedproj", "Feed Proj", "", strings.Repeat("z", 220)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	br, done := sseConnect(t, srv.URL, eventbus.TopicAgent(id))
	defer done()

	tk, err := s.CreateTicket(authed, domain.CreateTicketInput{ProjectIDOrSlug: "feedproj", Title: "Streamed Work"})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	lines := readUntil(t, br, "Streamed Work")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "#agent-activity-feed") {
		t.Errorf("activity patch missing #agent-activity-feed target:\n%s", joined)
	}
	if !strings.Contains(joined, "mode prepend") {
		t.Errorf("activity row should prepend:\n%s", joined)
	}
	if !strings.Contains(joined, "created a ticket") {
		t.Errorf("activity row missing the created verb:\n%s", joined)
	}

	// A comment by the same agent also prepends, rendered through the comment partial.
	if _, err := s.CreateComment(authed, tk.ID, "a streamed agent note"); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	clines := readUntil(t, br, "a streamed agent note")
	cjoined := strings.Join(clines, "\n")
	if !strings.Contains(cjoined, "#agent-activity-feed") || !strings.Contains(cjoined, "mode prepend") {
		t.Errorf("comment activity should prepend to the feed:\n%s", cjoined)
	}
	if !strings.Contains(cjoined, "commented") {
		t.Errorf("comment activity row missing the commented verb:\n%s", cjoined)
	}
}
