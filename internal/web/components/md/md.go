// Package md exposes the markdown renderer as a templ component so page
// templates can drop rendered markdown into their output without importing
// the parent web package (which would cycle web → components/pages → web).
//
// The implementation mirrors internal/web/markdown.go — same goldmark config,
// same "raw HTML escaped" threat model. It's duplicated rather than imported
// to break the import cycle; both copies are small enough that drift is easy
// to spot.
package md

import (
	"bytes"
	"context"
	"html/template"
	"io"
	"sync"

	"github.com/a-h/templ"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

var markdownRenderer = sync.OnceValue(func() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
			// Deliberately NO html.WithUnsafe — keeps raw HTML escaped.
		),
		goldmark.WithExtensions(
			extension.GFM,
		),
	)
})

// Render converts a markdown source to safe HTML. On parse failure (which
// goldmark essentially never does) falls back to the source wrapped in <pre>
// with HTML-escaping so the user sees their text rather than a blank page.
func Render(src string) template.HTML {
	if src == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := markdownRenderer().Convert([]byte(src), &buf); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(src) + "</pre>")
	}
	return template.HTML(buf.String())
}

// MD returns a templ.Component that emits the rendered markdown into the
// templ output stream. Use as `@md.MD(someBody)` inside .templ files.
func MD(src string) templ.Component {
	html := Render(src)
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, string(html))
		return err
	})
}
