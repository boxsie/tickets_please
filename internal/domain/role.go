package domain

// roleRank orders roles for "at least" comparisons. Higher = more privileged.
// An unknown role ranks 0 so it never satisfies a real minimum.
func roleRank(r Role) int {
	switch r {
	case RoleViewer:
		return 1
	case RoleMember:
		return 2
	case RoleOwner:
		return 3
	default:
		return 0
	}
}

// Satisfies reports whether a holder of role r meets the minimum role `min`.
// owner ⊇ member ⊇ viewer. Used by the web layer's per-project route guards.
func (r Role) Satisfies(min Role) bool {
	return roleRank(r) >= roleRank(min)
}
