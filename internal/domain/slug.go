package domain

import (
	"strings"
	"unicode"
)

// slugMaxLen caps slugified strings at 48 chars per T05 SPEC. Project slugs
// are validated separately; this helper is for ticket and phase
// directory-name generation.
const slugMaxLen = 48

// MakeSlug converts an arbitrary title string into a lowercase, dash-separated
// ASCII slug suitable for embedding in a directory name. Non-alphanumeric
// runs become a single dash, leading/trailing dashes are trimmed, and the
// result is capped at slugMaxLen runes (no mid-rune split since input is
// folded to ASCII first).
//
// Returns "untitled" if the input would slugify to empty (e.g. all-emoji
// title).
func MakeSlug(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevDash := true
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(unicode.ToLower(r))
			prevDash = false
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled"
	}
	if len(out) > slugMaxLen {
		out = strings.TrimRight(out[:slugMaxLen], "-")
		if out == "" {
			return "untitled"
		}
	}
	return out
}
