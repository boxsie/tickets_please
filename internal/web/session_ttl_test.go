package web

import (
	"testing"

	"tickets_please/internal/config"
)

// TestUserCookieMaxAge pins the web sign-in window contract (ticket 95b00884):
// auth.session_max_age_hours > 0 → that many hours; 0 or negative → indefinite.
func TestUserCookieMaxAge(t *testing.T) {
	cases := []struct {
		hours int
		want  int
	}{
		{0, indefiniteCookieMaxAge},  // default: indefinite
		{-5, indefiniteCookieMaxAge}, // negative also treated as indefinite
		{1, 60 * 60},
		{24, 24 * 60 * 60},
		{168, 168 * 60 * 60}, // a week
	}
	for _, c := range cases {
		a := &app{deps: Deps{Cfg: config.Config{Auth: config.AuthConfig{SessionMaxAgeHours: c.hours}}}}
		if got := a.userCookieMaxAge(); got != c.want {
			t.Errorf("SessionMaxAgeHours=%d → userCookieMaxAge()=%d, want %d", c.hours, got, c.want)
		}
	}
}
