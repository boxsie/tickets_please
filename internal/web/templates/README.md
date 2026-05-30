# `internal/web/templates/` — legacy `html/template` pages

This directory hosts the original `html/template` UI. It is being replaced
during Phase 011 (Frontend modernisation foundation) with typed templ
components under `internal/web/components/`. Both stacks render side-by-side
during the W1 migration; this directory is deleted in
[[delete-old-html-template-plumbing]] once every page is ported.

## templ generation policy (decided in [[templ-tooling]])

Generated `*_templ.go` files **are committed to the repo**, alongside their
`.templ` sources. Reasons:

- `go build` works for anyone with just the Go toolchain — no separate
  `go install github.com/a-h/templ/cmd/templ` step.
- The generated code is reviewable in PRs (catches accidentally-shipped
  unsafe interpolations or template bugs that `templ generate` would have
  surfaced).
- Single-binary builds (the whole point of this project) don't depend on a
  build-time tool being present in CI/release images.

Trade-off: `.templ` and `_templ.go` must stay in sync. The `make build`
target runs `make generate` first so local builds never miss this; CI
checks (`go run ./cmd/tickets_please check`) cover the rest.

To regenerate after editing a `.templ`:

```sh
make templ     # one-shot
make dev       # `templ --watch` alongside `serve --dev`
```
