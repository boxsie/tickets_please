package projects

import (
	"strconv"
	"strings"
)

// rating.go: helpers for the inline search-hit rating widget (rating.templ).

// ratingWidgetID is the stable DOM id for one hit's rating widget. Entry keys
// are `<kind>:<id>`; the colon is swapped for a dash so the id is a valid CSS
// selector target for hx-swap (htmx resolves the target via querySelector).
func ratingWidgetID(entryKey string) string {
	return "hit-rating-" + strings.ReplaceAll(entryKey, ":", "-")
}

// ratingTarget is ratingWidgetID as a `#id` selector for hx-target.
func ratingTarget(entryKey string) string {
	return "#" + ratingWidgetID(entryKey)
}

// searchRateHref is the POST endpoint the rating forms submit to.
func searchRateHref(slug string) string {
	return "/p/" + slug + "/search/rate"
}

// ratedGlyph maps the stored rating to the glyph shown in the sticky rated
// state. Anything other than "dislike" reads as a like.
func ratedGlyph(ratedAs string) string {
	if ratedAs == "dislike" {
		return "\U0001F44E" // 👎
	}
	return "\U0001F44D" // 👍
}

// itoa keeps the templ call sites free of the strconv import.
func itoa(n int) string { return strconv.Itoa(n) }
