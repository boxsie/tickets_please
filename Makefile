.PHONY: build build-windows run test init-config init-data check

BIN := tickets_please

build:
	go build -o $(BIN) ./cmd/tickets_please

# build-windows cross-compiles for Windows. The file-lock layer is the one
# GOOS-specific piece (flock on unix, LockFileEx on windows, build-tagged in
# internal/store/lock_*.go); this target keeps the Windows build from
# regressing without a Windows runner.
build-windows:
	GOOS=windows GOARCH=amd64 go build ./...

run:
	go run ./cmd/tickets_please mcp

test:
	go test ./...

init-config:
	mkdir -p ~/.tickets_please
	cp -n examples/config.yaml ~/.tickets_please/config.yaml || true

init-data:
	mkdir -p .tickets_please/.staging
	mkdir -p ~/.tickets_please/agents ~/.tickets_please/.staging

check: build-windows
	go run ./cmd/tickets_please check
