package web

import (
	"net/http"
	"strings"

	"tickets_please/internal/web/components/pages"
	"tickets_please/internal/web/components/partials"
)

// handleLogs renders the in-process log ring as a plain <pre> block. The
// page auto-tails via htmx polling against the same handler with HX-Request
// set, returning just the inner LogsPre fragment so the swap doesn't drag
// the chrome along. When the ring isn't wired (e.g. in tests that don't
// care), the page renders an empty buffer.
func (a *app) handleLogs(w http.ResponseWriter, r *http.Request) {
	var lines []string
	if a.deps.Logs != nil {
		snap := a.deps.Logs.Snapshot()
		lines = make([]string, len(snap))
		for i, b := range snap {
			lines[i] = string(b)
		}
	}
	props := partials.LogsPreProps{
		Lines: strings.Join(lines, "\n"),
		Count: len(lines),
		Empty: len(lines) == 0,
	}
	if r.Header.Get("HX-Request") == "true" {
		a.renderer.RenderTemplPartial(w, r, partials.LogsPre(props))
		return
	}
	a.renderer.RenderTempl(w, r, PageOpts{
		Title: "Logs · tickets_please",
	}, pages.Logs(props))
}
