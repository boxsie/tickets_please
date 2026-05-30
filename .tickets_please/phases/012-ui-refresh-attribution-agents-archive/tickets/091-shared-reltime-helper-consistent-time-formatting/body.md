Right now comments format times one way, the project overview another, the activity list yet another. Standardise.

## Acceptance

- New `internal/web/reltime/reltime.go` with:
  - `Short(t time.Time, now time.Time) string` — "just now" / "5m ago" / "3h ago" / "yesterday" / "Mar 4" / "Mar 4, 2025" (year only when not current).
  - `Absolute(t time.Time) string` — ISO 8601 with TZ for `title=` attributes.
  - `Long(t time.Time) string` — e.g. "Jan 2 · 15:04" for explicit time displays (matches the current comment format).
- A `Time(t time.Time)` templ component renders `<time datetime="...">{short}</time>` with the absolute in `title`. Used everywhere.
- All template touch-points migrated: comments, tickets, recent activity, learnings, phase headers, agent last-seen.
- Tests cover boundary cases (just-now, 59m, 1h, 23h, 1d, 6d, last year).

## Hints

- Accept `now` as a parameter (not `time.Now()` inside) so tests are deterministic; default to `time.Now()` in a thin wrapper.
- Hour boundaries: <60s "just now", <60m "Nm ago", <24h "Nh ago", <7d "Nd ago", same year "Mon D", else "Mon D, YYYY".
