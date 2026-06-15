## Testing evidence
Ran `make build`, `go test ./internal/web`, `git diff --check`, and a generated-CSS probe confirming `--accent-fg:#f8fcff`, `.btn-primary:hover`, `.sidebar`, and `.project-picker>summary` rules. Started a temporary local server on 127.0.0.1:8766 and captured `/tmp/tp-controls-overview.png` plus `/tmp/tp-controls-settings.png` with headless Google Chrome for visual smoke testing.

## Work summary
Updated the CSS source and regenerated embedded CSS to modernize CTA contrast/hover, sidebar navigation, project picker/dropdowns, app/project nav links, tabs, data tables, form inputs/selects, board inline filters, assign-phase selects, and filesystem picker rows. CTA text now uses light text with explicit hover/active states instead of filter-only hover styling.

## Learnings
The sidebar and shared controls are still centralized in `internal/web/static/_src/app.css`; regenerate `internal/web/static/app.css` with `make build` or `make css`. CTA hover should be styled explicitly (`.btn-primary:hover` preserving white text) rather than relying on filter brightening. Screenshot both an overview page and a controls-heavy settings page when changing the shared nav/control system.
