Lay the templ foundation. Nothing visible changes — this just unlocks every subsequent migration ticket.

## Acceptance

- `github.com/a-h/templ` added to `go.mod`; `templ` CLI installable via `go install`.
- `//go:generate templ generate` directive at `internal/web/` package root; `go generate ./...` regenerates `_templ.go` files.
- `make templ` target added; `make build` runs it (or depends on a `make generate` umbrella).
- `make dev` runs `templ generate --watch` alongside the Go server with live reload (use `air` since it's already on PATH per CLAUDE.md, or document `make watch` if simpler).
- Generated `*_templ.go` files are gitignored OR checked in — pick one and document the choice in `internal/web/templates/README.md` (recommendation: check in, so `go build` works without templ installed).
- One throwaway templ component (`internal/web/components/hello_templ.templ` rendering `&lt;h1&gt;hello&lt;/h1&gt;`) compiles and is reachable from a `/_dev/templ-hello` route gated behind `deps.Dev`.

## Hints

- templ docs: https://templ.guide/
- Mirror the `web/templates/` layout under `internal/web/components/`; the existing html/template files stay live during W1's migration.
