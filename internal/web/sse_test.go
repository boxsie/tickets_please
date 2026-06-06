package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/auth"
	"tickets_please/internal/domain"
	"tickets_please/internal/eventbus"
	"tickets_please/internal/store"
	"tickets_please/internal/svc"
)

// sseConnect opens a streaming /sse request and returns a line reader plus a
// cancel to tear the stream down. It blocks until the server has written its
// ": connected" comment, which (per handleSSE) happens only after Subscribe —
// so the caller can publish without racing the subscription.
func sseConnect(t *testing.T, baseURL, topics string) (*bufio.Reader, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/sse?topics="+topics, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("sse connect: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("sse status = %d, want 200", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)
	readUntil(t, br, ": connected")
	return br, func() { cancel(); resp.Body.Close() }
}

// readUntil scans lines until one contains substr or a short deadline elapses.
// Returns the accumulated lines for assertions.
func readUntil(t *testing.T, br *bufio.Reader, substr string) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lines []string
	type res struct {
		line string
		err  error
	}
	for time.Now().Before(deadline) {
		ch := make(chan res, 1)
		go func() {
			l, err := br.ReadString('\n')
			ch <- res{l, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("sse read: %v (so far: %v)", r.err, lines)
			}
			line := strings.TrimRight(r.line, "\n")
			lines = append(lines, line)
			if strings.Contains(line, substr) {
				return lines
			}
		case <-time.After(time.Until(deadline)):
			t.Fatalf("timed out waiting for %q; got: %v", substr, lines)
		}
	}
	t.Fatalf("deadline waiting for %q; got: %v", substr, lines)
	return lines
}

func sseTestServer(t *testing.T) (*httptest.Server, *eventbus.Bus) {
	t.Helper()
	srv, bus, _ := sseTestServerDeps(t)
	return srv, bus
}

// sseTestServerDeps also wires the service's publisher to the bus (so svc
// mutations fan out through /sse) and returns the Deps for driving the
// service directly.
func sseTestServerDeps(t *testing.T) (*httptest.Server, *eventbus.Bus, Deps) {
	t.Helper()
	deps := freshDeps(t)
	bus := eventbus.NewBus()
	deps.Bus = bus
	deps.Service.SetPublisher(bus)
	mux := http.NewServeMux()
	Mount(mux, deps)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, bus, deps
}

func TestSSE_SubscribePublishReceive(t *testing.T) {
	srv, bus := sseTestServer(t)
	br, done := sseConnect(t, srv.URL, "ticket:t1")
	defer done()

	bus.Publish(eventbus.Event{
		Kind:      eventbus.KindCommentAdded,
		Topics:    []string{eventbus.TopicTicket("t1")},
		TicketID:  "t1",
		CommentID: "c1",
	})

	// The baseline signal frame carries the ticket id; assert we receive it.
	lines := readUntil(t, br, `"ticketId":"t1"`)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "event: "+eventbus.TopicGlobalAgents) && // sanity: not a global frame
		!strings.Contains(joined, "datastar-patch-signals") {
		t.Errorf("expected a datastar-patch-signals frame, got:\n%s", joined)
	}
	if !strings.Contains(joined, "id: 1") {
		t.Errorf("expected frame stamped with seq id 1, got:\n%s", joined)
	}
}

func TestSSE_ReconnectReplaysNewerOnly(t *testing.T) {
	srv, bus := sseTestServer(t)

	// Publish two events with nobody listening — they buffer in the ring.
	for range 2 {
		bus.Publish(eventbus.Event{Kind: eventbus.KindTicketMoved, Topics: []string{eventbus.TopicTicket("t1")}, TicketID: "t1", ToColumn: "in_progress"})
	}

	// Reconnect "having seen seq 1": expect a replay of seq 2 only.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/sse?topics=ticket:t1", nil)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	readUntil(t, br, ": connected")
	lines := readUntil(t, br, "id: 2")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "id: 1") {
		t.Errorf("replay leaked already-seen seq 1:\n%s", joined)
	}
}

func TestSSE_TicketDetailLivePatches(t *testing.T) {
	srv, _, deps := sseTestServerDeps(t)
	s := deps.Service

	agentID, _, err := s.RegisterAgent(context.Background(), "live-fixture", "Claude Live", nil, time.Hour, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(context.Background(), agentID)
	if _, err := s.CreateProject(authed, "live", "Live", "", strings.Repeat("z", 220)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	tk, err := s.CreateTicket(authed, domain.CreateTicketInput{ProjectIDOrSlug: "live", Title: "Realtime ticket"})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	br, done := sseConnect(t, srv.URL, "ticket:"+tk.ID)
	defer done()

	// Move → status badge re-renders to the new column + a toast appears.
	if _, err := s.MoveTicket(authed, tk.ID, domain.ColumnInProgress, "starting work"); err != nil {
		t.Fatalf("MoveTicket: %v", err)
	}
	lines := readUntil(t, br, `id="ticket-status"`)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "datastar-patch-elements") {
		t.Errorf("move patch not a datastar-patch-elements frame:\n%s", joined)
	}
	if !strings.Contains(joined, "badge-in_progress") {
		t.Errorf("status badge didn't re-render to in_progress:\n%s", joined)
	}
	// The action cluster patch and the toast ride the same delivery.
	more := readUntil(t, br, "Moved to in_progress")
	if !strings.Contains(strings.Join(more, "\n"), "ticket-actions") &&
		!strings.Contains(joined, "ticket-actions") {
		t.Errorf("expected a #ticket-actions patch around the move")
	}

	// Comment → row appended to #comments-list.
	if _, err := s.CreateComment(authed, tk.ID, "a streamed comment"); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	clines := readUntil(t, br, "comments-list")
	cjoined := strings.Join(clines, "\n")
	if !strings.Contains(cjoined, "mode append") {
		t.Errorf("comment patch should append:\n%s", cjoined)
	}
	commentRow := readUntil(t, br, "comment-row")
	if !strings.Contains(strings.Join(commentRow, "\n"), "a streamed comment") {
		t.Errorf("appended comment row missing body:\n%s", strings.Join(commentRow, "\n"))
	}

	// Archive → archived badge appears.
	if _, err := s.ArchiveTicket(authed, tk.ID, "shelving"); err != nil {
		t.Fatalf("ArchiveTicket: %v", err)
	}
	alines := readUntil(t, br, "badge-archived")
	if !strings.Contains(strings.Join(alines, "\n"), "ticket-archived") {
		t.Errorf("archived badge patch missing #ticket-archived wrapper:\n%s", strings.Join(alines, "\n"))
	}
}

func TestSSE_OptimisticCommentReconcile(t *testing.T) {
	srv, _, deps := sseTestServerDeps(t)
	s := deps.Service

	agentID, _, err := s.RegisterAgent(context.Background(), "opt-fixture", "Claude Opt", nil, time.Hour, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(context.Background(), agentID)
	if _, err := s.CreateProject(authed, "optlive", "Opt Live", "", strings.Repeat("z", 220)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	tk, err := s.CreateTicket(authed, domain.CreateTicketInput{ProjectIDOrSlug: "optlive", Title: "Opt ticket"})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	br, done := sseConnect(t, srv.URL, "ticket:"+tk.ID)
	defer done()

	// A comment created with a client id (as the web handler does from the
	// Idempotency-Key header) should make the SSE echo first remove the
	// optimistic placeholder, then append the canonical row.
	const cid = "cid-abc-123"
	if _, err := s.CreateComment(svc.WithClientID(authed, cid), tk.ID, "reconciled comment"); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	rm := readUntil(t, br, "#comment-pending-"+cid)
	joined := strings.Join(rm, "\n")
	if !strings.Contains(joined, "mode remove") {
		t.Errorf("expected a mode-remove frame for the optimistic placeholder:\n%s", joined)
	}
	appended := readUntil(t, br, "reconciled comment")
	if !strings.Contains(strings.Join(appended, "\n"), "mode append") {
		t.Errorf("expected the canonical row to append after the remove:\n%s", strings.Join(appended, "\n"))
	}
}

func TestSSE_PhasesPageLivePatches(t *testing.T) {
	srv, _, deps := sseTestServerDeps(t)
	s := deps.Service

	agentID, _, err := s.RegisterAgent(context.Background(), "phase-fixture", "Claude Phase", nil, time.Hour, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(context.Background(), agentID)
	if _, err := s.CreateProject(authed, "phaselive", "Phase Live", "", strings.Repeat("z", 220)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	phase, err := s.CreatePhase(authed, "phaselive", "Backbone", "", strings.Repeat("p", 220))
	if err != nil {
		t.Fatalf("CreatePhase: %v", err)
	}
	tk, err := s.CreateTicket(authed, domain.CreateTicketInput{
		ProjectIDOrSlug: "phaselive",
		Title:           "Phased ticket",
		PhaseIDOrSlug:   &phase.Slug,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	// The phases-index page subscribes to project:{id}.
	br, done := sseConnect(t, srv.URL, "project:"+tk.ProjectID)
	defer done()

	// Move → the phase's wave list re-renders (new dot/badge) and the meta
	// cell (bar + counts) rebalances.
	if _, err := s.MoveTicket(authed, tk.ID, domain.ColumnInProgress, "starting"); err != nil {
		t.Fatalf("MoveTicket: %v", err)
	}
	lines := readUntil(t, br, "#phase-waves-"+phase.ID)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "datastar-patch-elements") {
		t.Errorf("phase patch not a datastar-patch-elements frame:\n%s", joined)
	}
	if !strings.Contains(joined, "mode inner") {
		t.Errorf("phase wave-list patch should inner-morph:\n%s", joined)
	}
	rows := readUntil(t, br, "badge-in_progress")
	if !strings.Contains(strings.Join(rows, "\n"), "ticket-row-"+tk.ID) {
		t.Errorf("re-rendered wave list missing the moved ticket row:\n%s", strings.Join(rows, "\n"))
	}
	meta := readUntil(t, br, "#phase-meta-"+phase.ID)
	bar := readUntil(t, br, "phase-row-bar-in_progress")
	if !strings.Contains(strings.Join(append(meta, bar...), "\n"), "phase-row-bar-in_progress") {
		t.Errorf("phase meta bar didn't rebalance to in_progress:\n%s", strings.Join(bar, "\n"))
	}

	// Create a second ticket in the same phase → it streams into the wave list.
	tk2, err := s.CreateTicket(authed, domain.CreateTicketInput{
		ProjectIDOrSlug: "phaselive",
		Title:           "Another phased ticket",
		PhaseIDOrSlug:   &phase.Slug,
	})
	if err != nil {
		t.Fatalf("CreateTicket 2: %v", err)
	}
	created := readUntil(t, br, "ticket-row-"+tk2.ID)
	if !strings.Contains(strings.Join(created, "\n"), "#phase-waves-"+phase.ID) {
		t.Errorf("created ticket didn't ride a #phase-waves patch:\n%s", strings.Join(created, "\n"))
	}
}

func TestSSE_CrossTenantTopicRejected(t *testing.T) {
	deps := freshDeps(t)
	bus := eventbus.NewBus()
	deps.Bus = bus
	a := newApp(deps)
	a.authEnabled = true // force the membership-gated path

	// Seed: user U is a member of project P1 but NOT P2.
	ctx := context.Background()
	if _, err := deps.Service.MembershipStore.GrantMembership(ctx, &store.MembershipRecord{
		UserID: "u1", ProjectID: "p1", Role: domain.RoleViewer, GrantedAt: time.Now(),
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	user := &domain.User{ID: "u1"}

	t.Run("own project allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/sse?topics=project:p1", nil)
		req = req.WithContext(auth.WithUser(req.Context(), user))
		topics, err := a.authorizeTopics(req)
		if err != nil {
			t.Fatalf("expected p1 allowed, got %v", err)
		}
		if len(topics) != 1 || topics[0] != "project:p1" {
			t.Fatalf("topics = %v", topics)
		}
	})

	t.Run("other tenant rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/sse?topics=project:p2", nil)
		req = req.WithContext(auth.WithUser(req.Context(), user))
		if _, err := a.authorizeTopics(req); err == nil {
			t.Fatal("expected forbidden for project:p2, got nil")
		}
	})

	t.Run("global agents allowed for any authed user", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/sse?topics="+eventbus.TopicGlobalAgents, nil)
		req = req.WithContext(auth.WithUser(req.Context(), user))
		if _, err := a.authorizeTopics(req); err != nil {
			t.Fatalf("global:agents should be allowed: %v", err)
		}
	})
}
