package web

import (
	"net/http"
	"strings"
)

// logsPageData is the payload for pages/logs.tmpl. Lines are pre-joined into
// a single string so the template can drop the whole buffer into a <pre>
// without per-iteration overhead.
type logsPageData struct {
	Lines string
	Count int
	Empty bool
}

// handleLogs renders the in-process log ring as a plain <pre> block. Auto-
// refreshes via meta-refresh (set in the template). When the ring isn't
// wired (e.g. in tests that don't care), the page renders an empty buffer.
func (a *app) handleLogs(w http.ResponseWriter, r *http.Request) {
	var lines []string
	if a.deps.Logs != nil {
		snap := a.deps.Logs.Snapshot()
		lines = make([]string, len(snap))
		for i, b := range snap {
			lines[i] = string(b)
		}
	}
	data := logsPageData{
		Lines: strings.Join(lines, "\n"),
		Count: len(lines),
		Empty: len(lines) == 0,
	}
	a.renderer.Page(w, r, "logs", PageOpts{
		Title: "Logs · tickets_please",
		Body:  data,
	})
}
