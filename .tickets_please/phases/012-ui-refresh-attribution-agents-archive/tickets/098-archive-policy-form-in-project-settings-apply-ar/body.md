The archive-policy schema is in `project.yaml` but the settings page doesn't expose it. Add a form, plus a dry-run button that surfaces what would be archived without applying.

## Acceptance

- Project settings page gains an "Archive policy" section with form fields:
  - `enabled` (checkbox)
  - `min_age_days` (number)
  - `min_retrievals` (number)
  - `dislike_ratio` (number 0..1)
  - `early_archive_age_days` (number)
  - `auto_sweep_on_mount` (checkbox)
- Save → POST to existing project-settings handler.
- A "Preview policy (dry-run)" button calls the existing `apply_archive_policy` service method with `commit=false`; renders the resulting list of tickets the sweep would archive (with the reason: age vs ratio) below the form.
- An "Apply now" button (owner-only) calls the same with `commit=true`, shows the actual sweep report.
- Both reports include un-archived counts so the user knows the baseline.
- Tests cover: form save round-trip; dry-run renders; apply-now writes.

## Hints

- The dry-run report is small enough to render inline; no need to navigate away.
- Reuse the existing settings form layout — this is a new fieldset, not a new page.
