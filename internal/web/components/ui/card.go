package ui

import "strings"

// CardProps is the typed input to the Card component. Title is optional —
// when set it renders an <h2> at the top, matching the existing
// .card > h2 convention from app.css's @layer components block.
type CardProps struct {
	Title   string
	Compact bool
	Class   string
	// Href, when set, renders a right-aligned action link in the card header
	// (e.g. "Browse all phases →"). LinkLabel is its text; it defaults to
	// "View all →" when Href is set but LinkLabel is empty.
	Href      string
	LinkLabel string
}

// CardLinkLabel is the header-link text for a Card — the explicit LinkLabel, or
// a sensible default when only Href is supplied.
func CardLinkLabel(p CardProps) string {
	if p.LinkLabel != "" {
		return p.LinkLabel
	}
	return "View all →"
}

func cardClass(p CardProps) string {
	parts := []string{"card"}
	if p.Compact {
		parts = append(parts, "card-compact")
	}
	if p.Class != "" {
		parts = append(parts, p.Class)
	}
	return strings.Join(parts, " ")
}
