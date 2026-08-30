package web

import (
	"net/http"
	"net/url"
)

// Per-user "show ideas" preference — the idea-lane sibling of
// archived_pref.go. Idea-kind tickets are hidden from the default work surfaces
// (the service post-filters them); this is the single global toggle that brings
// the ideas lane into view. A tiny cookie (tp_show_ideas=1) makes it stick
// across navigation, per-user not per-project, mirroring how the MCP treats
// include_ideas.

const showIdeasCookie = "tp_show_ideas"

// showIdeasCookieMaxAge is one year — "remember until flipped back". A homelab
// convenience, not security-sensitive.
const showIdeasCookieMaxAge = 365 * 24 * 60 * 60

// resolveShowIdeas returns whether the ideas lane should be shown, honouring
// (in order): an explicit ?include_ideas= on this request, else the persisted
// cookie, else false. When the request carries the param the choice is written
// back to the cookie so it sticks on later navigation.
func (a *app) resolveShowIdeas(w http.ResponseWriter, r *http.Request) bool {
	if vals, ok := r.URL.Query()["include_ideas"]; ok {
		on := false
		for _, v := range vals {
			if v == "true" || v == "1" || v == "on" {
				on = true
			}
		}
		setShowIdeasCookie(w, on)
		return on
	}
	c, err := r.Cookie(showIdeasCookie)
	return err == nil && c.Value == "1"
}

// setShowIdeasCookie persists the preference. on→"1" with a long TTL; off→an
// immediately-expiring cookie so the value clears rather than lingering as "0"
// (resolveShowIdeas treats anything but "1" as off anyway).
func setShowIdeasCookie(w http.ResponseWriter, on bool) {
	c := &http.Cookie{
		Name:     showIdeasCookie,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if on {
		c.Value = "1"
		c.MaxAge = showIdeasCookieMaxAge
	} else {
		c.Value = ""
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// ideasToggleHref builds the URL the show-ideas toggle links to: the current
// request URL with include_ideas flipped. Cloning the query preserves every
// other filter (?wave, ?phase, ?q, ?include_archived…) so toggling never drops
// the user's context, and the link works with no JS.
func ideasToggleHref(r *http.Request, currentlyShown bool) string {
	q := r.URL.Query()
	if currentlyShown {
		q.Set("include_ideas", "false")
	} else {
		q.Set("include_ideas", "true")
	}
	u := url.URL{Path: r.URL.Path, RawQuery: q.Encode()}
	return u.RequestURI()
}
