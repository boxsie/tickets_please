package web

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tickets_please/internal/auth"
	"tickets_please/internal/domain"
	"tickets_please/internal/eventbus"
	"tickets_please/internal/web/sse"
)

// heartbeatInterval is the gap between SSE comment frames. Most proxies and
// load balancers close idle long-poll connections somewhere between 30s and
// 60s; 25s sits below that floor while staying cheap on bandwidth.
const heartbeatInterval = 25 * time.Second

// maxTopicsPerConnection caps the topic list a single /sse connection may
// request — a cheap guard against a client asking to fan in thousands of
// topics on one socket.
const maxTopicsPerConnection = 64

// handleSSE holds an SSE connection open for the lifetime of the request,
// streaming Datastar patches derived from eventbus deliveries on the
// subscribed topics, interleaved with periodic heartbeats. Exits cleanly when
// the client disconnects (request context cancels) or when the bus
// disconnects a slow consumer (done closes).
//
// Topics are requested via ?topics=project:abc,ticket:xyz and authorized
// against the caller's project memberships (when auth is enabled). On
// reconnect the client's Last-Event-ID header drives a replay of buffered
// events newer than that seq before live attach.
//
// Proxy note: a buffering reverse proxy (default nginx, default HAProxy in
// http mode) WILL stall this stream. We set X-Accel-Buffering: no for
// nginx-compatible proxies; other layers need their own streaming config.
func (a *app) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	topics, err := a.authorizeTopics(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// No bus or no topics → keep the stream open with heartbeats only, so the
	// page's subscription and any reverse proxy stay happy.
	if a.deps.Bus == nil || len(topics) == 0 {
		sse.WriteComment(w, "connected (heartbeat-only)")
		flusher.Flush()
		a.runHeartbeatOnly(r, w, flusher)
		return
	}

	lastSeq := parseLastEventID(r.Header.Get("Last-Event-ID"))
	replay, live, done, cancel := a.deps.Bus.Subscribe(topics, lastSeq)
	defer cancel()

	sse.WriteComment(w, "connected")
	// Replay buffered events the client missed, in seq order, before live.
	for _, d := range replay {
		a.writeDelivery(r.Context(), w, d)
	}
	flusher.Flush()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-done:
			// Bus disconnected us (slow consumer). The client reconnects with
			// Last-Event-ID and resumes from the ring buffer.
			return
		case <-ticker.C:
			sse.WriteComment(w, "heartbeat")
			flusher.Flush()
		case d, ok := <-live:
			if !ok {
				return
			}
			a.writeDelivery(r.Context(), w, d)
			flusher.Flush()
		}
	}
}

// writeDelivery renders one eventbus delivery into its Datastar patch frame(s)
// and writes them, stamping each with the event seq as the SSE id so a
// reconnect can resume from it.
func (a *app) writeDelivery(ctx context.Context, w io.Writer, d eventbus.Delivery) {
	id := strconv.FormatUint(d.Event.Seq, 10)
	for _, ev := range a.renderDelivery(ctx, d) {
		ev.ID = id
		sse.Write(w, ev)
	}
}

// runHeartbeatOnly handles the no-bus / no-topics path.
func (a *app) runHeartbeatOnly(r *http.Request, w io.Writer, flusher http.Flusher) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			sse.WriteComment(w, "heartbeat")
			flusher.Flush()
		}
	}
}

// parseLastEventID parses the SSE Last-Event-ID header (the seq of the last
// frame the client saw). Returns 0 on absence or garbage — a fresh attach.
func parseLastEventID(s string) uint64 {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// authorizeTopics parses ?topics= and validates the caller may subscribe to
// each. When auth is disabled (localhost no-auth mode) every topic is allowed.
// When enabled, project:/ticket: topics require >=viewer membership on the
// resolved project; phase: topics ride on a co-requested authorized project;
// agent:/global:agents are app-global and allowed for any authenticated user.
//
// Returns the cleaned, deduped topic list. An empty ?topics= yields no topics
// (a heartbeat-only stream), not an error.
func (a *app) authorizeTopics(r *http.Request) ([]string, error) {
	raw := strings.Split(r.URL.Query().Get("topics"), ",")
	seen := make(map[string]struct{}, len(raw))
	var topics []string
	for _, t := range raw {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		topics = append(topics, t)
		if len(topics) > maxTopicsPerConnection {
			return nil, errSSE("too many topics requested")
		}
	}

	if !a.authEnabled || len(topics) == 0 {
		return topics, nil
	}

	user, ok := auth.UserFrom(r.Context())
	if !ok {
		return nil, errSSE("authentication required")
	}

	// First pass: which projects can this user see? Used to ride phase topics.
	allowedProjects := make(map[string]struct{})
	authorize := func(projectID string) bool {
		if _, ok := allowedProjects[projectID]; ok {
			return true
		}
		mem, err := a.deps.Service.MembershipStore.GetMembership(projectID, user.ID)
		if err != nil || !mem.Role.Satisfies(domain.RoleViewer) {
			return false
		}
		allowedProjects[projectID] = struct{}{}
		return true
	}

	var phaseTopics []string
	for _, t := range topics {
		switch {
		case strings.HasPrefix(t, "project:"):
			if !authorize(strings.TrimPrefix(t, "project:")) {
				return nil, errSSE("forbidden topic: " + t)
			}
		case strings.HasPrefix(t, "ticket:"):
			tkt, err := a.deps.Service.GetTicket(r.Context(), strings.TrimPrefix(t, "ticket:"))
			if err != nil || !authorize(tkt.ProjectID) {
				return nil, errSSE("forbidden topic: " + t)
			}
		case t == eventbus.TopicGlobalAgents || strings.HasPrefix(t, "agent:"):
			// App-global agent registry; any authenticated user may watch.
		case strings.HasPrefix(t, "phase:"):
			phaseTopics = append(phaseTopics, t) // resolved after the loop
		default:
			return nil, errSSE("unknown topic: " + t)
		}
	}
	// A phase topic is authorized only if the same connection also holds an
	// authorized project topic — phase-detail always pairs them. This avoids a
	// project-wide phase scan for a marginal case.
	if len(phaseTopics) > 0 && len(allowedProjects) == 0 {
		return nil, errSSE("forbidden topic: " + phaseTopics[0])
	}

	return topics, nil
}

type sseError string

func (e sseError) Error() string { return string(e) }
func errSSE(msg string) error    { return sseError(msg) }
