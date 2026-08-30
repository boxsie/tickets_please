package reltime

import (
	"testing"
	"time"
)

func TestShort(t *testing.T) {
	now := time.Date(2026, time.June, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"just now (0s)", now, "just now"},
		{"just now (30s)", now.Add(-30 * time.Second), "just now"},
		{"59m", now.Add(-59 * time.Minute), "59m ago"},
		{"1h", now.Add(-1 * time.Hour), "1h ago"},
		{"23h", now.Add(-23 * time.Hour), "23h ago"},
		{"1d -> yesterday", now.Add(-24 * time.Hour), "yesterday"},
		{"47h -> yesterday", now.Add(-47 * time.Hour), "yesterday"},
		{"2d", now.Add(-48 * time.Hour), "2d ago"},
		{"6d", now.Add(-6 * 24 * time.Hour), "6d ago"},
		{"this year date", now.Add(-30 * 24 * time.Hour), "May 7"},
		{"last year", time.Date(2025, time.March, 4, 9, 0, 0, 0, time.UTC), "Mar 4, 2025"},
		{"future collapses", now.Add(5 * time.Minute), "just now"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Short(c.t, now); got != c.want {
				t.Fatalf("Short(%v) = %q, want %q", c.t, got, c.want)
			}
		})
	}
}

func TestAbsolute(t *testing.T) {
	ts := time.Date(2026, time.June, 6, 12, 34, 56, 0, time.UTC)
	if got, want := Absolute(ts), "2026-06-06T12:34:56Z"; got != want {
		t.Fatalf("Absolute = %q, want %q", got, want)
	}
}

func TestLong(t *testing.T) {
	ts := time.Date(2026, time.January, 2, 15, 4, 0, 0, time.UTC)
	if got, want := Long(ts), "Jan 2 · 15:04"; got != want {
		t.Fatalf("Long = %q, want %q", got, want)
	}
}
