Last cleanup of W1. After every page is templ-rendered, the old plumbing is dead weight — remove it so the codebase doesn't drift into two-stacks-forever.

## Acceptance

- `internal/web/templates/` directory deleted (or kept empty + gitignored if `embed.FS` build complains; favour full deletion).
- Old template-function helpers (`markdown`, `derefCol`, `phaseSlug`, `mkAssignPhase`, `mkTicketCard`, `hasSuffix`, `percentOf`, etc.) either deleted or re-exposed as plain Go funcs callable from templ.
- `internal/web/render.go`'s `Render(...)` for html/template removed; only the templ render path remains.
- `embed.FS` for templates removed; static-only `embed.FS` retained.
- All tests pass; no dead imports; `go vet ./...` clean.
- `make build` produces a binary the same size or smaller than before W1.

## Hints

- Run `go mod tidy` after the deletion — `html/template` import will likely drop from the indirect graph if nothing else uses it.
- Watch for tests that grep specific raw template output — update them to grep the templ-rendered equivalent.
