package web

import (
	"net/http"
	"net/url"
)

// Per-user "show archived" preference. Archived tickets are hidden by default
// across the list/search surfaces (the service post-filters them out); this is
// the single global toggle that brings them back. The preference is a tiny
// cookie (tp_show_archived=1) so it survives navigation, and it's per-user not
// per-project — one switch for the whole UI, mirroring how the MCP spec treats
// include_archived.

const showArchivedCookie = "tp_show_archived"

// showArchivedCookieMaxAge is one year — effectively "remember until the user
// flips it back". A homelab convenience, not a security-sensitive value.
const showArchivedCookieMaxAge = 365 * 24 * 60 * 60

// resolveShowArchived returns whether archived items should be shown, honouring
// (in order): an explicit ?include_archived= on this request, else the
// persisted cookie, else false. When the request carries the param the choice
// is written back to the cookie so it sticks on later navigation.
//
// The param is read via the multi-value form so the link-toggle's explicit
// true/false always resolves correctly (and a stray duplicate where any value
// is truthy wins).
func (a *app) resolveShowArchived(w http.ResponseWriter, r *http.Request) bool {
	if vals, ok := r.URL.Query()["include_archived"]; ok {
		on := false
		for _, v := range vals {
			if v == "true" || v == "1" || v == "on" {
				on = true
			}
		}
		setShowArchivedCookie(w, on)
		return on
	}
	c, err := r.Cookie(showArchivedCookie)
	return err == nil && c.Value == "1"
}

// setShowArchivedCookie persists the preference. on→"1" with a long TTL;
// off→an immediately-expiring cookie so the value clears rather than lingering
// as "0" (resolveShowArchived treats anything but "1" as off anyway).
func setShowArchivedCookie(w http.ResponseWriter, on bool) {
	c := &http.Cookie{
		Name:     showArchivedCookie,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if on {
		c.Value = "1"
		c.MaxAge = showArchivedCookieMaxAge
	} else {
		c.Value = ""
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// archivedToggleHref builds the URL the include-archived toggle links to: the
// current request URL with include_archived flipped to the opposite of the
// current state. Cloning the existing query preserves every other filter
// (?wave, ?phase, ?q, ?kind…) so toggling never drops the user's context, and
// the link stays shareable + works with no JS.
func archivedToggleHref(r *http.Request, currentlyShown bool) string {
	q := r.URL.Query()
	if currentlyShown {
		q.Set("include_archived", "false")
	} else {
		q.Set("include_archived", "true")
	}
	u := url.URL{Path: r.URL.Path, RawQuery: q.Encode()}
	return u.RequestURI()
}
