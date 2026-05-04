package web

import (
	"bytes"
	"html/template"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// renderMarkdown returns sanitised HTML for a markdown source.
//
// Goldmark's default config does NOT enable html.WithUnsafe, so any raw HTML
// in the source is escaped rather than passed through. That's the threat
// model here: project/phase/ticket summary fields are user input rendered
// inside a server-trusted page, and we don't want a malicious summary to
// inject script tags. With raw HTML disabled, what comes out is markdown
// constructs (headings, lists, code, links, emphasis) and nothing else —
// safe to feed into template.HTML.
//
// The renderer is constructed once and shared (Markdown rendering is
// goroutine-safe per the goldmark docs).
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
			extension.GFM, // tables, strikethrough, autolinks, task lists
		),
	)
})

// renderMarkdown converts the source string to safe HTML. Non-fatal: on
// parse failure (which goldmark essentially never does for any valid bytes),
// returns the original source HTML-escaped.
func renderMarkdown(src string) template.HTML {
	if src == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := markdownRenderer().Convert([]byte(src), &buf); err != nil {
		// Fall back to plain text wrapped in a <pre> so the user still sees
		// their source rather than a blank page. template.HTMLEscapeString
		// keeps it safe.
		return template.HTML("<pre>" + template.HTMLEscapeString(src) + "</pre>")
	}
	return template.HTML(buf.String())
}
