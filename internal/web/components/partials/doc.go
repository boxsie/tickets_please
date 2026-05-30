// Package partials hosts standalone templ partial components — fragments that
// handlers render through Renderer.RenderTemplPartial (or, for the error
// component, RenderTemplError). They sit alongside pages/ but are *not* full
// pages: no layout, no chrome, just the swap target.
//
// Each partial is a 1:1 port of the legacy templates/partials/*.tmpl with the
// same semantic class names, hx-* attributes, and copy. The old .tmpl files
// stay on disk until the cleanup ticket retires the html/template pipeline.
package partials

//go:generate go run github.com/a-h/templ/cmd/templ generate
