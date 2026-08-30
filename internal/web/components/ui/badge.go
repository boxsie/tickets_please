package ui

// BadgeVariant matches the existing @layer-components badge classes
// (badge-todo, badge-in_progress, …) so server-rendered Column values map
// 1:1 onto a typed variant. The four column variants are the canonical
// palette established in [[tailwind-v4]]'s --color-badge-* theme tokens.
type BadgeVariant string

const (
	BadgeVariantTodo       BadgeVariant = "todo"
	BadgeVariantInProgress BadgeVariant = "in_progress"
	BadgeVariantTesting    BadgeVariant = "testing"
	BadgeVariantDone       BadgeVariant = "done"
	BadgeVariantSystem     BadgeVariant = "system"
	BadgeVariantBlocked    BadgeVariant = "blocked"
)

// BadgeProps is the typed input to the Badge component.
type BadgeProps struct {
	Variant BadgeVariant
	Label   string
}

// badgeClass returns the composed class string for a Badge. Keeping the
// switch here means renaming a variant doesn't ripple through every call
// site — only the constant + this case statement change.
func badgeClass(v BadgeVariant) string {
	switch v {
	case BadgeVariantTodo:
		return "badge badge-todo"
	case BadgeVariantInProgress:
		return "badge badge-in_progress"
	case BadgeVariantTesting:
		return "badge badge-testing"
	case BadgeVariantDone:
		return "badge badge-done"
	case BadgeVariantSystem:
		return "badge badge-system"
	case BadgeVariantBlocked:
		return "badge badge-blocked"
	default:
		return "badge"
	}
}
