package web

import (
	"fmt"
	"net/http"
	"time"

	"tickets_please/internal/eventbus"
	"tickets_please/internal/web/components"
	"tickets_please/internal/web/components/ui"
)

// devPingAgentID tags the synthetic AgentSeen event the dev SSE-ping button
// publishes, so the agent-patch renderer can route it to #sse-target instead
// of a real agent row.
const devPingAgentID = "dev-ping"

// devPingSpan is the element fragment the dev ping morphs into #sse-target.
func devPingSpan(label string) string {
	return `<span id="sse-target" class="badge badge-done">` + label + `</span>`
}

// handleTemplHello renders the throwaway components.Hello templ component as
// the smoke proof that the .templ → _templ.go → render pipeline is alive.
// Wired only when deps.Dev is true (see router.go). Delete with the rest of
// the /_dev/* dev scaffolding once Wave 1's real pages render via templ.
func (a *app) handleTemplHello(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := components.Hello("templ").Render(r.Context(), w); err != nil {
		a.deps.Logger.Error("dev: render templ hello", "err", err)
	}
}

// handleComponentsPlayground renders ui.Playground — every variant of every
// templ component in internal/web/components/ui/ on one scrollable page.
// The visual-regression smoke test during the Wave 1 page migration: open
// /_dev/components on the dev server and eyeball that primitives still
// render the way pages expect them to.
//
// Dev-only (wired in router.go under `if deps.Dev`). Production builds with
// --dev=false return 404 for this path.
func (a *app) handleComponentsPlayground(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := ui.Playground().Render(r.Context(), w); err != nil {
		a.deps.Logger.Error("dev: render components playground", "err", err)
	}
}

// handleSSEPing publishes one datastar-patch-elements event to the global
// SSE topic — the playground's "SSE ping" button hits this and Datastar
// merges the returned <span> into #sse-target by id. Proves the full pipe
// (button click → POST → Hub.Publish → /sse → Datastar runtime → DOM patch)
// without committing to any production event format.
//
// Dev-only — see router.go's `if deps.Dev` gate.
func (a *app) handleSSEPing(w http.ResponseWriter, r *http.Request) {
	if a.deps.Bus == nil {
		http.Error(w, "sse bus not wired", http.StatusServiceUnavailable)
		return
	}
	a.deps.Bus.Publish(eventbus.Event{
		Kind:      eventbus.KindAgentSeen,
		Topics:    []string{eventbus.TopicGlobalAgents},
		AgentID:   devPingAgentID,
		AgentName: fmt.Sprintf("pong @ %s", time.Now().UTC().Format(time.RFC3339Nano)),
	})
	w.WriteHeader(http.StatusNoContent)
}
