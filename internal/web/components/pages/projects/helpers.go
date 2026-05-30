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
