package web

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"tickets_please/internal/web/sse"
)

// heartbeatInterval is the gap between SSE comment frames. Most proxies and
// load balancers close idle long-poll connections somewhere between 30s and
// 60s; 25s sits below that floor while staying cheap on bandwidth.
const heartbeatInterval = 25 * time.Second

// topicGlobal is the single fan-out topic during W1. Per-session topic
// scoping arrives in the sse-hub-per-session-topic-scoped follow-up; until
// then every subscriber sees every event.
const topicGlobal = "global"

// handleSSE holds an SSE connection open for the lifetime of the request,
// streaming events from the Hub interleaved with periodic heartbeats. Exits
// cleanly when the client disconnects (request context cancels) or when the
// hub closes its subscriber channel.
//
// Proxy note: a buffering reverse proxy (default nginx, default HAProxy in
// http mode) WILL stall this stream. Disable response buffering on the proxy
// side or run the server behind one that streams by default — we explicitly
// set X-Accel-Buffering: no for nginx-compatible proxies, but other layers
// need their own config.
func (a *app) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if a.deps.Hub == nil {
		writeSSEComment(w, "hub not wired; stream is heartbeat-only")
		flusher.Flush()
		runHeartbeatOnly(r, w, flusher)
		return
	}

	events, cancel := a.deps.Hub.Subscribe(topicGlobal)
	defer cancel()

	writeSSEComment(w, "connected")
	flusher.Flush()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			writeSSEComment(w, "heartbeat")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			writeSSEEvent(w, ev)
			flusher.Flush()
		}
	}
}

// runHeartbeatOnly handles the no-Hub path: keep the stream open with just
// heartbeats until the client disconnects. Lets the page subscribe and the
// proxy stay happy even when the server isn't wired to publish.
func runHeartbeatOnly(r *http.Request, w io.Writer, flusher http.Flusher) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			writeSSEComment(w, "heartbeat")
			flusher.Flush()
		}
	}
}

// writeSSEComment writes a `: <text>` line. SSE comments are ignored by the
// EventSource API but keep the connection alive on the wire.
func writeSSEComment(w io.Writer, text string) {
	fmt.Fprintf(w, ": %s\n\n", text)
}

// writeSSEEvent serialises ev into the on-wire SSE frame: optional `id:`,
// optional `event:`, then one `data:` line per line of ev.Data, terminated by
// the required blank line.
func writeSSEEvent(w io.Writer, ev sse.Event) {
	if ev.ID != "" {
		fmt.Fprintf(w, "id: %s\n", ev.ID)
	}
	if ev.Type != "" {
		fmt.Fprintf(w, "event: %s\n", ev.Type)
	}
	for _, line := range strings.Split(ev.Data, "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}
