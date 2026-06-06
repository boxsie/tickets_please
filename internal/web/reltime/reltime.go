// Package reltime centralises human-facing time formatting across the web UI.
//
// Before this package, comments, the project overview, and the activity list
// each rolled their own formatter. Everything now funnels through Short (the
// relative "5m ago" label), Long (an explicit "Jan 2 · 15:04" stamp), and
// Absolute (a machine ISO-8601 value for title=/datetime= attributes).
//
// The formatters take `now` explicitly so tests stay deterministic; the
// *Now wrappers and the Time templ component fill it in with time.Now().
package reltime

import (
	"fmt"
	"time"
)

// Short renders a compact relative label for t as observed at now:
// "just now", "5m ago", "3h ago", "yesterday", "3d ago", "Mar 4"
// (current year), or "Mar 4, 2025" (other years). Future times collapse
// to "just now" — they shouldn't occur on real data.
func Short(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	default:
		return t.Format("Jan 2, 2006")
	}
}

// ShortNow is Short relative to the current wall-clock time.
func ShortNow(t time.Time) string { return Short(t, time.Now()) }

// Absolute renders t as an ISO-8601 string with timezone offset, suitable
// for <time datetime="..."> and title= tooltips.
func Absolute(t time.Time) string { return t.Format("2006-01-02T15:04:05Z07:00") }

// Long renders an explicit "Jan 2 · 15:04" stamp — matches the legacy
// comment-thread format for places that show a precise time rather than a
// relative one.
func Long(t time.Time) string { return t.Format("Jan 2 · 15:04") }
