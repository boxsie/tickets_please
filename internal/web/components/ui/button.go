package ui

import (
	"strings"

	"github.com/a-h/templ"
)

// ButtonVariant is the enum-like string identifying a button's visual role.
// The switch in buttonClass keeps the class strings in one place — call sites
// pass a typed constant, never a raw class name.
type ButtonVariant string

const (
	ButtonVariantPrimary   ButtonVariant = "primary"
	ButtonVariantSecondary ButtonVariant = "secondary"
	ButtonVariantDanger    ButtonVariant = "danger"
	ButtonVariantGhost     ButtonVariant = "ghost"
	ButtonVariantLink      ButtonVariant = "link"
)

// ButtonSize is the enum-like string identifying a button's size. Empty
// string == default ("md") so zero-value ButtonProps renders a standard btn.
type ButtonSize string

const (
	ButtonSizeMd ButtonSize = ""
	ButtonSizeSm ButtonSize = "sm"
)

// ButtonProps is the typed input to the Button component.
//
//   - Href set ≠ "" renders as <a> (anchor button); otherwise renders <button>.
//   - Type defaults to "button" for plain <button> renders — explicit so a
//     button inside a form never accidentally submits.
//   - Class is an escape hatch for one-off extras (e.g. "ml-auto") layered on
//     after the variant classes.
type ButtonProps struct {
	Variant  ButtonVariant
	Size     ButtonSize
	Type     string // "button" (default), "submit", "reset"
	Label    string // optional — if Children isn't used, falls back to this
	Href     string // if set, renders as <a>
	Disabled bool
	Class    string
	// Attrs is a passthrough for arbitrary HTML attributes — most commonly
	// data-dialog / hx-* / aria-* that the variant table doesn't model.
	Attrs templ.Attributes
}

// buttonClass composes the Tailwind / @layer-components class string for a
// Button. Kept in Go (not inlined in .templ) so the switch is one canonical
// table: variants and sizes are added or renamed in one place.
func buttonClass(p ButtonProps) string {
	parts := []string{"btn"}
	switch p.Variant {
	case ButtonVariantPrimary:
		parts = append(parts, "btn-primary")
	case ButtonVariantDanger:
		parts = append(parts, "btn-danger")
	case ButtonVariantLink:
		parts = append(parts, "btn-link")
	case ButtonVariantSecondary, ButtonVariantGhost, "":
		// .btn alone is the ghost/secondary visual default.
	}
	if p.Size == ButtonSizeSm {
		parts = append(parts, "btn-sm")
	}
	if p.Class != "" {
		parts = append(parts, p.Class)
	}
	return strings.Join(parts, " ")
}

// buttonType normalises Type so the generated HTML always has an explicit
// attribute, avoiding the implicit "submit" default that bites when a button
// sits inside a form.
func buttonType(t string) string {
	if t == "" {
		return "button"
	}
	return t
}
