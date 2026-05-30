package web

import (
	"fmt"
	"net/http"
	"time"

	"tickets_please/internal/web/components"
	"tickets_please/internal/web/components/ui"
	"tickets_please/internal/web/sse"
)

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
	if a.deps.Hub == nil {
		http.Error(w, "sse hub not wired", http.StatusServiceUnavailable)
		return
	}
	body := fmt.Sprintf(
		`elements <span id="sse-target" class="badge badge-done">pong @ %s</span>`,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	a.deps.Hub.Publish("global", sse.Event{
		Type: "datastar-patch-elements",
		Data: body,
	})
	w.WriteHeader(http.StatusNoContent)
}
