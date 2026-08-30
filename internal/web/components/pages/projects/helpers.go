package projects

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/a-h/templ"
)

// segmentWidth returns the inline `width: N%` style for one status-bar
// segment. Kept as a small Go helper so the templ source stays free of
// fmt.Sprintf noise; the inline width is the documented escape hatch from
// detail.tmpl (server-computed percentage, not a static utility class).
func segmentWidth(percent int) string {
	return "width: " + strconv.Itoa(percent) + "%"
}

// segmentTitle is the hover-title for one status-bar segment — matches the
// legacy template's `{{.Label}}: {{.Count}} ({{.Percent}}%)` formatting
// verbatim.
func segmentTitle(seg StatusSegment) string {
	return fmt.Sprintf("%s: %d (%d%%)", seg.Label, seg.Count, seg.Percent)
}

// donePercentHint renders the sub-label on the "Done" metric card — the share
// of the project that's complete, e.g. "62% complete". Falls back to a static
// "completed" when there are no tickets to divide by.
func donePercentHint(m DashboardMetrics) string {
	if m.Total <= 0 {
		return "completed"
	}
	return strconv.Itoa(m.Done*100/m.Total) + "% complete"
}

// unphasedHint renders the sub-label on the "Phases" metric card, noting how
// many tickets sit outside any phase (the bucket worth surfacing on an
// overview). Reads "all tickets phased" when none are loose.
func unphasedHint(unphased int) string {
	if unphased <= 0 {
		return "all tickets phased"
	}
	return strconv.Itoa(unphased) + " unphased"
}

// ideasLaneTitle renders the Ideas lane card title with a count, e.g.
// "Ideas (3)" — the spitball backlog, separate from the work board.
func ideasLaneTitle(n int) string {
	if n == 0 {
		return "Ideas"
	}
	return "Ideas (" + strconv.Itoa(n) + ")"
}

// ideasHiddenHint is the muted line shown when the ideas lane is collapsed.
func ideasHiddenHint(n int) string {
	if n == 1 {
		return "1 idea hidden. "
	}
	return strconv.Itoa(n) + " ideas hidden. "
}

// confirmDelete builds the inline JS the danger-zone form uses to ask "are
// you sure?" before POSTing the delete. Slugs are already URL-safe
// (`[a-z0-9-]+`) so the JS-string escape is belt-and-braces — kept anyway so
// nothing breaks if the slug rules ever loosen.
func confirmDelete(slug string) templ.ComponentScript {
	js := "return confirm('Delete project " + jsString(slug) +
		" and ALL its phases/tickets/comments? This cannot be undone.');"
	return templ.JSUnsafeFuncCall(js)
}

// jsString escapes a runtime string for embedding into a JS single-quoted
// literal.
func jsString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return s
}
