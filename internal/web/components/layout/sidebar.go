package layout

import (
	"strings"

	"tickets_please/internal/domain"
)

// activeProject returns the project matching data.CurrentSlug, or nil if no
// project is selected (or the slug doesn't match anything mounted). The
// sidebar uses this to gate the per-project nav vs the "no project selected"
// hint.
func activeProject(data PageData) *domain.Project {
	for _, p := range data.Chrome.Projects {
		if p.Slug == data.CurrentSlug {
			return p
		}
	}
	return nil
}

// navLinkClass returns the class string for a per-project nav anchor,
// appending `active` when the current request URL matches.
//
// Matching strategy: the "Overview" link uses exact-equals (the URL is just
// `/p/<slug>`); every other link uses suffix-match (e.g. `/phases`) since the
// project slug prefix is the same for all of them.
func navLinkClass(base, currentURL, match string, exact bool) string {
	if linkIsActive(currentURL, match, exact) {
		return base + " active"
	}
	return base
}

// linkIsActive returns true when the current URL should mark a nav link as
// active. Split out so the templ can both pick the class AND emit the
// aria-current="page" attribute from the same predicate.
func linkIsActive(currentURL, match string, exact bool) bool {
	if exact {
		return currentURL == match
	}
	return strings.HasSuffix(currentURL, match)
}
