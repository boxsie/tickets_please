// Package ui hosts the reusable templ component library used by every
// migrated page in the Wave 1 frontend modernisation. Each component takes a
// typed *Props struct (no interface{}) and encapsulates its Tailwind / @layer
// classes so the call site is just data, never styling.
//
// Variants are enum-like string types — the switch lives inside one Go helper
// (button.go, badge.go, …) and the .templ files just consume the result. This
// is the "cva-in-Go" pattern called out by the ticket; see button.go for the
// canonical shape.
//
// To regenerate after editing a .templ:
//
//	make templ        # one-shot
//	make dev          # templ --watch alongside the server
package ui

//go:generate go run github.com/a-h/templ/cmd/templ generate
