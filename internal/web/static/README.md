# Web UI static assets

Files in this directory are baked into the binary via `go:embed` from
`../dev.go` and served under `/static/` by the web router. The `_src/` folder
holds the Tailwind sources used to regenerate `app.css` — Go's embed
deliberately skips `_`-prefixed entries so those sources stay out of the
binary.

## Files

| File              | Purpose                                                       |
|-------------------|---------------------------------------------------------------|
| `app.css`         | Compiled, minified stylesheet. Regenerate via the command below. |
| `htmx.min.js`     | Vendored htmx 1.9.12, used for partial swaps.                 |
| `htmx.LICENSE`    | 0BSD licence text for htmx.                                   |
| `_src/input.css`  | Tailwind v3 source: `@tailwind` directives + hand-authored components layer. |
| `_src/tailwind.config.js` | Tailwind config: dark colour palette, content globs, safelist. |

## Regenerating `app.css`

Day-to-day work doesn't touch CSS — the chrome is locked in for the
web-frontend phase. When a template needs a new class, edit `_src/input.css`
(or add the class somewhere Tailwind's `content` glob will pick up) and run:

```sh
cd internal/web/static
npx -y tailwindcss@3.4.10 -c ./_src/tailwind.config.js -i ./_src/input.css -o ./app.css --minify
```

Commit the regenerated `app.css` alongside the source change. There is
intentionally no Tailwind step in the build pipeline — the binary stays
single-artifact, no Node toolchain.

## Why so small?

Templates use semantic classes (`.topbar`, `.sidebar`, `.empty-state`) rather
than Tailwind utilities (`.flex .gap-4 .px-3`). The compiled output is
~8 KB instead of the ~30 KB a utility-heavy build produces, because Tailwind
purges every utility it can't see in the templates and we don't reference
many. The hand-authored `@layer components` block in `_src/input.css` is what
templates actually use — Tailwind itself is essentially an opinionated PostCSS
preprocessor with `theme(...)` colour resolution and a normalise/preflight.
That setup is enough to keep design tokens in one place without imposing the
utility-class style on every template.

If a future ticket finds itself reaching for `.flex .gap-4 .text-sm` etc., go
ahead — Tailwind will regenerate them on the next CSS build. The components
layer is meant for *chrome*, not for one-offs.
