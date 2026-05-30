Add Tailwind v4 with a `make` build step that emits a single CSS bundle into `internal/web/static/`. No Node at runtime — Node is a dev-time dependency only.

## Acceptance

- Tailwind v4 standalone CLI (or via npm in `internal/web/static/_src/`) wired into a `make css` target.
- `internal/web/static/_src/app.css` is the source; `internal/web/static/app.css` is the generated output (embedded via the existing `embed.FS` in `internal/web/static`).
- Tailwind config defines a small palette aligned with current colours (`badge-todo`, `badge-in_progress`, `badge-testing`, `badge-done` — pull current values from `internal/web/static/app.css`).
- A `tickets_please` theme layer captures the design tokens so component classes can stay declarative.
- `make css` runs as part of `make build`; the embedded CSS in committed `app.css` always matches source (CI check: rebuild and `git diff --exit-code app.css`).
- Hand-rolled CSS removed from inline `&lt;style&gt;` blocks in templates — anything left in `app.css` is either Tailwind-output or one annotated escape hatch.

## Hints

- Tailwind v4 standalone CLI: https://tailwindcss.com/blog/standalone-cli
- Keep `_src/app.css` to ~20 lines: `@import 'tailwindcss'` + a few `@layer components { … }` blocks for badges/cards.
- This ticket overlaps with [[component-library-skeleton]] — coordinate so the component lib uses Tailwind classes from day one.
