.PHONY: build run test init-config init-data check

BIN := tickets_please

build:
	go build -o $(BIN) ./cmd/tickets_please

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

check:
	go run ./cmd/tickets_please check
