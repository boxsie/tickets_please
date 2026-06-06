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
	deps := freshDeps(t)
	bus := eventbus.NewBus()
	deps.Bus = bus
	mux := http.NewServeMux()
	Mount(mux, deps)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, bus
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
