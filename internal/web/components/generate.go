// Package components hosts typed templ components for the web UI.
//
// Files ending in .templ are processed by `templ generate` (see Makefile) into
// sibling *_templ.go files. Both the .templ sources and the generated .go are
// committed: this keeps `go build` working without the templ CLI present, and
// the generated files are reviewable in PRs.
//
// To regenerate locally after editing a .templ:
//
//	make templ        # one-shot
//	make dev          # templ --watch alongside the server
package components

//go:generate go run github.com/a-h/templ/cmd/templ generate
