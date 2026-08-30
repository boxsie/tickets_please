// Package pages hosts page-level templ components. See home.templ for the
// canonical shape — each page is a `templ Name(...)` invoked by a handler
// through Renderer.RenderTempl, which wraps it in the layout.Layout chrome.
package pages

//go:generate go run github.com/a-h/templ/cmd/templ generate
