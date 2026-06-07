## Testing evidence
Before/after headless-Chrome crops of the search-hit rating widget: 👍 and 👎 now render as identical flat glyphs (no blue box).

## Learnings
Two reusable lessons from the blue-thumbs bug. (1) The app deliberately styles bare `button[type="submit"]:not(.btn):not(.btn-link)` as PRIMARY (accent fill) — so ANY submit button without `.btn` inherits a blue fill + button base-shape, and that selector's specificity (0,3,1) beats a lone utility class like `.hit-rate-btn` (0,1,0). If you want a flat/custom submit button, you must EXCLUDE it from those four global selectors (`:not(.your-class)`), not just set `background:transparent` on it. (2) `--bg-elev-1` does NOT exist — the tokens are `--bg-elev` and `--bg-elev-2`; an undefined CSS var makes the whole declaration drop silently. Grep for `--bg-elev-1` before reusing. Both bugs were invisible in the source CSS and only showed under a real browser — screenshot-verify rating/0-chrome controls.
