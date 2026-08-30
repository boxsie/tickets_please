// Package layout hosts the top-level templ layout, top bar, sidebar, and
// project picker — the chrome that wraps every templ-rendered page in the
// Wave 1 frontend migration.
//
// To avoid a `web → components/layout → web` import cycle, this package
// declares its own PageData / Chrome / Flash mirror types that capture the
// subset of state the chrome templates actually read. The Renderer in
// internal/web converts its private web.PageData into a layout.PageData on
// every RenderTempl call.
package layout

import "tickets_please/internal/domain"

// PageData is the chrome-shaped payload the Layout templ consumes. It mirrors
// web.PageData minus the html/template-specific Body field — templ pages take
// their own typed props directly, no opaque .Body bag.
type PageData struct {
	Title       string
	CurrentSlug string
	Chrome      Chrome
}

// Chrome mirrors web.Chrome. Same fields, no behaviour — exists only so the
// templ layer can name a struct without importing web (which would cycle).
type Chrome struct {
	Projects        []*domain.Project
	AgentLabel      string
	Flash           *Flash
	CSRF            string
	ShowLocalBanner bool
	// URL is the request path (no query string). The sidebar uses it to
	// highlight the active per-project nav item via suffix-match.
	URL string
}

// Flash mirrors web.Flash. Kind is "success" | "info" | "error".
type Flash struct {
	Kind    string
	Message string
}
