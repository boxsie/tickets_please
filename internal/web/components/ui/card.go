package ui

import "strings"

// CardProps is the typed input to the Card component. Title is optional —
// when set it renders an <h2> at the top, matching the existing
// .card > h2 convention from app.css's @layer components block.
type CardProps struct {
	Title   string
	Compact bool
	Class   string
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
