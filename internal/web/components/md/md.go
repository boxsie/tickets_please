// Package md is a tiny templ-friendly markdown helper. Pages that render
// user-supplied markdown call md.MD(src) to drop a sanitised HTML fragment
// into the templ output.
//
// The renderer matches internal/web/markdown.go's goldmark config (GFM, hard
// wraps, raw HTML kept escaped) so the rendered output is identical to what
// the legacy html/template `markdown` funcMap entry produced. Keeping the
// goldmark setup here (rather than depending on web) avoids a
// web → components/* → web import cycle.
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
			// Deliberately NO html.WithUnsafe — raw HTML stays escaped.
		),
		goldmark.WithExtensions(
			extension.GFM,
		),
	)
})

// Render converts a markdown source string to safe HTML, matching
// internal/web/markdown.go's behaviour 1:1 (so the legacy and templ paths
// produce byte-identical output for the same input).
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

// MD returns a templ.Component that writes the rendered markdown straight
// into the page. Use from a .templ as `@md.MD(value)`. The output is already
// sanitised by Render (raw HTML kept escaped) so it's safe to splat without
// re-escaping.
func MD(src string) templ.Component {
	html := Render(src)
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, string(html))
		return err
	})
}
