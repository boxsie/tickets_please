## Goal

The Global Settings page (W5-T3) writes `~/.tickets_please/config.yaml`. The user explicitly wants comments preserved in that file across saves.

## Scope

- New `internal/config/save.go` exporting:
  ```go
  func SaveYAMLNode(path string, modify func(*yaml.Node) error) error
  ```
  Implementation: read file → unmarshal into `*yaml.Node` → invoke `modify` (which targets specific scalar nodes by key path) → marshal → atomic write (tmp + rename, same pattern as `vecindex/persist.go:WriteSidecar`).
- Helper for the common case: `SetScalar(root *yaml.Node, key string, value string) error` that walks a top-level mapping and updates the Value of the existing scalar node, preserving its head/foot/line comments. Adds the key if missing (with no comment).
- Use `gopkg.in/yaml.v3`. Confirm it's already in `go.mod` (transitively pulled in via Go's mcp library load); add if not.

## Tests

- `internal/config/save_test.go`:
  - Round-trip a fixture file with comments above each key, between sections, and trailing — assert comments + key order survive after a `SetScalar` mutation.
  - Adding a missing key appends at the end without comment.
  - Atomic write: a mid-write crash (simulated by erroring `modify`) leaves the original file intact.

## Done when

- `make build` + `go test ./...` green.
- Fixture round-trip diff is byte-identical except for the targeted scalar value.
