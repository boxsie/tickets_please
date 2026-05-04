## Goal

Re-shape the sidebar so it serves both "switch project" and "navigate within the active project" — without doubling the rail width or sacrificing the active-project highlight. Three changes:

1. The flat list of projects becomes a **searchable combobox** at the top of the sidebar. Clicking the toggle reveals a filterable list plus inline "+ New project" and "+ Load existing" actions.
2. Below the picker, when a project is active, render a **persistent per-project menu** (Board / Phases / Waves / Summary / New ticket / Edit). Active item highlighted by URL match.
3. When no project is active, the picker is the only thing in the rail with a short empty-state pitch.

## Why

Current pain points:
- The Board / Phases / Waves / Summary tabs live ONLY on the project detail page (`/p/{slug}`). Once you click into the board or a phase or a ticket, you lose the navigation. Going from "look at a ticket on the board" to "view the project's phases" requires multiple back-clicks.
- The project list is a flat sidebar that gets long once you mount more than ~10 projects.
- "+ New project" and "+ Load existing" sit in the sidebar as muted text-prefix links that don't really belong with project navigation — they're chrome-level actions.

The picker + per-project nav puts the right thing in the right place: switching is one control, navigating-within is the rest of the rail.

## Scope

### Picker (top of sidebar)

- A `<details>`-based combobox so the open/close behavior is native and works without JS framework.
- Toggle button shows the current project's name (or "Pick a project" when none active) plus a chevron.
- Open state: search input + filterable scrollable list of projects + divider + "+ New project" / "+ Load existing" actions.
- Search filter is client-side (small inline script — no roundtrip needed for typical <50-project lists).
- Click-outside-to-close via a small inline script.
- Active project highlighted in the list when the dropdown is open.

### Per-project nav (rest of sidebar, only when a project is active)

Items:
- Board (`/p/{slug}/board`)
- Phases (`/p/{slug}/phases`)
- Waves (`/p/{slug}/waves`)
- Summary (`/p/{slug}/summary`)
- New ticket (`/p/{slug}/tickets/new`) — visually separated as the primary action
- Edit project (`/p/{slug}/edit`) — muted secondary

Active item highlighted by URL match. The chrome assembly puts `r.URL.Path` on Chrome so the template can do suffix-matching without per-handler section codes.

### Empty state

When no project is currently selected (sidebar shows the picker but no project context), render a small "No project selected — pick one above or create a new one" hint.

### Templates

- Restructure `partials/sidebar.tmpl` end-to-end.
- New `partials/project_picker.tmpl` — the dropdown combobox.
- Drop the old `.sidebar-actions` block (its actions move into the picker).

### Plumbing

- Add `URL string` to `web.Chrome`. Render-time set from `r.URL.Path` in the chrome assembly.
- Add a `hasSuffix` template func.
- The existing `sidebar-refresh` HX-Trigger contract still applies — POST /p, POST /p/load, POST /p/{slug}/delete already trigger it; the picker will re-render via the same /_partials/sidebar endpoint.

### CSS

New classes:
- `.project-picker`, `.project-picker-toggle`, `.project-picker-dropdown`, `.project-picker-search`, `.project-picker-list`, `.picker-action`, `.picker-chevron`
- `.project-nav`, `.project-nav-link`, `.project-nav-link.active`
- All safelisted in tailwind.config.js.

## Verification

- Unit tests: chrome carries URL; project-picker partial renders with current project highlighted; per-project nav highlights the right item per URL.
- E2E: Playwright walkthrough updated with screenshots of (a) sidebar collapsed, (b) sidebar with picker open, (c) sidebar inside a project showing the per-project nav with active item.
- Manual: navigate from board → ticket → phases → search; sidebar always shows the project context plus the nav items; no accidental loss of "where am I".

## Out of scope

- Project favorites / pinning.
- Drag-to-reorder projects.
- Recently-viewed tracking.
- Keyboard shortcuts (`/` to focus search etc.) — defer.
- Mobile collapse behavior beyond what already exists.

## Notes

- One ticket inside this phase (the work is small but cohesive). Could be done in one PR.
- The `<details>` element is the right primitive for a combobox in a no-framework world; no need to bring in a JS dropdown library.
