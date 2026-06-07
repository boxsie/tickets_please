On search hits the 👍 had a bright-blue fill while the 👎 was flat.

Two compounding causes:
1. The 👍 is a `<button type="submit">` and the 👎 is a `<summary>`; the global rule `button[type="submit"]:not(.btn):not(.btn-link)` gives bare submit buttons the accent (blue) fill + button base-shape, and its specificity (0,3,1) beat `.hit-rate-btn` (0,1,0).
2. `.hit-rate-btn:hover` referenced an undefined `--bg-elev-1` (only `--bg-elev`/`--bg-elev-2` exist) — same typo also broke `.archived-toggle-box` and `.archive-report` backgrounds.

Fix: excluded `.hit-rate-btn` from the four global `button[type="submit"]` selectors, added `appearance:none` + flat transparent styling so the `<button>` and `<summary>` render identically, and corrected `--bg-elev-1` → `--bg-elev-2` everywhere.
