package web

import (
	"net/http"
	"sort"

	"tickets_please/internal/web/components/pages"
)

// handleHome handles GET /. Behaviour:
//
//   - If any projects are mounted, redirect to /p/<first-slug> (alphabetical
//     for determinism — first-mount-wins would surprise on restart).
//   - Otherwise render the templ Home page with an empty-state hint pointing
//     at /p/load and /p/new. The templ version composes the new layout +
//     sidebar chrome; the legacy pages/home.tmpl stays on disk until the
//     "delete old html/template plumbing" ticket retires it.
//
// Future tickets will register more specific patterns (/p/, /tickets/) on
// the mux; those preempt this catch-all for their prefixes.
func (a *app) handleHome(w http.ResponseWriter, r *http.Request) {
	// http.ServeMux's "/" pattern is a catch-all. Reject paths we don't own
	// so requests for, say, /favicon.ico or /robots.txt don't render the
	// home page with a 200.
	if r.URL.Path != "/" {
		a.renderer.Error(w, r, http.StatusNotFound, errNotFound{path: r.URL.Path})
		return
	}

	projects, err := a.deps.Service.ListProjects(r.Context())
	if err != nil {
		a.deps.Logger.Error("home: list projects", "err", err)
		a.renderer.Error(w, r, http.StatusInternalServerError, err)
		return
	}

	if len(projects) > 0 {
		slugs := make([]string, 0, len(projects))
		for _, p := range projects {
			slugs = append(slugs, p.Slug)
		}
		sort.Strings(slugs)
		http.Redirect(w, r, "/p/"+slugs[0], http.StatusSeeOther)
		return
	}

	a.renderer.RenderTempl(w, r, PageOpts{Title: "tickets_please"}, pages.Home())
}

type errNotFound struct{ path string }

func (e errNotFound) Error() string { return "not found: " + e.path }
