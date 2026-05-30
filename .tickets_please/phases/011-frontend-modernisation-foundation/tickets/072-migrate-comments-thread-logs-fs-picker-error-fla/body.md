Mop-up migration for everything else still on `html/template`. After this lands, no live page renders through the old renderer.

## Acceptance

- Ported: `logs.tmpl`, `settings.tmpl` (top-level), every remaining partial (`error.tmpl`, `flash.tmpl`, `fs_picker.tmpl`, `frozen_actions.tmpl`, `logs_pre.tmpl`).
- `handleLogs`, `handleGlobalSettings`, `handleFSBrowse` switched to templ.
- Audit: `grep -r "Renderer.Render\b" internal/web/` returns zero hits OR every remaining hit is the deprecated path slated for [[delete-old-html-template-plumbing]].
- All `internal/web/handlers_*_test.go` pass.

## Hints

- Logs page is intentionally minimal (`&lt;pre&gt;`) — easy migration.
- FS picker is the only one with non-trivial htmx interactivity (recursive directory listing) — preserve `hx-target` / `hx-swap` exactly.
