.PHONY: build build-windows run test init-config init-data check templ generate dev css

BIN := tickets_please

# generate is the umbrella for every code-gen step. New generators get a
# .PHONY target above and a line under here so a single `make generate`
# keeps everything in sync.
generate: templ css

# TEMPL is the templ CLI used to regenerate *_templ.go from *.templ. Always
# safe to re-run; output is deterministic and we commit it (see
# internal/web/components/generate.go for the rationale). If templ isn't on
# PATH, install it once with `go install github.com/a-h/templ/cmd/templ@latest`.
# Generated files are checked in, so `go build` works without templ installed —
# you only need it when editing .templ sources.
TEMPL ?= $(shell command -v templ 2>/dev/null)

templ:
	@if [ -z "$(TEMPL)" ]; then \
	  echo "templ CLI not found on PATH."; \
	  echo "install with: go install github.com/a-h/templ/cmd/templ@latest"; \
	  exit 1; \
	fi
	$(TEMPL) generate

# TAILWIND is the Tailwind v4 standalone CLI. Single binary, no Node, no
# npm — keeps the toolchain ethos of "fewer moving parts" intact. Download
# from https://github.com/tailwindlabs/tailwindcss/releases (binary name
# tailwindcss-linux-x64 / -darwin-arm64 / etc.; chmod +x and drop on PATH).
# The committed internal/web/static/app.css is the build output; the source
# lives at internal/web/static/_src/app.css and is the only thing humans
# should edit.
TAILWIND ?= $(shell command -v tailwindcss 2>/dev/null)

css:
	@if [ -z "$(TAILWIND)" ]; then \
	  echo "tailwindcss CLI not found on PATH."; \
	  echo "download from: https://github.com/tailwindlabs/tailwindcss/releases"; \
	  echo "  curl -sSL -o ~/go/bin/tailwindcss https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-linux-x64 && chmod +x ~/go/bin/tailwindcss"; \
	  exit 1; \
	fi
	$(TAILWIND) -i internal/web/static/_src/app.css -o internal/web/static/app.css --minify

build: generate
	go build -o $(BIN) ./cmd/tickets_please

# build-windows cross-compiles for Windows. The file-lock layer is the one
# GOOS-specific piece (flock on unix, LockFileEx on windows, build-tagged in
# internal/store/lock_*.go); this target keeps the Windows build from
# regressing without a Windows runner.
build-windows: generate
	GOOS=windows GOARCH=amd64 go build ./...

run: generate
	go run ./cmd/tickets_please mcp

# dev runs the server with --dev (on-disk template reload) alongside
# `templ generate --watch`, so editing a .templ regenerates and a browser
# refresh shows it. The trap kills the watcher when the foreground server
# exits so Ctrl-C cleans up both halves.
dev:
	@if [ -z "$(TEMPL)" ]; then \
	  echo "templ CLI not found; install: go install github.com/a-h/templ/cmd/templ@latest"; \
	  exit 1; \
	fi
	@echo "starting templ --watch + serve --dev (ctrl-c to stop both)"
	@$(TEMPL) generate --watch & \
	  TEMPL_PID=$$!; \
	  trap "kill $$TEMPL_PID 2>/dev/null || true" EXIT INT TERM; \
	  go run ./cmd/tickets_please serve --dev

test: generate
	go test ./...

init-config:
	mkdir -p ~/.tickets_please
	cp -n examples/config.yaml ~/.tickets_please/config.yaml || true

init-data:
	mkdir -p .tickets_please/.staging
	mkdir -p ~/.tickets_please/agents ~/.tickets_please/.staging

check: build-windows
	go run ./cmd/tickets_please check
