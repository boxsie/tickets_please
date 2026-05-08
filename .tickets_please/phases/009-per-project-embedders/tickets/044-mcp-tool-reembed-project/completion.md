## Testing evidence
make build clean; go test ./... all green. New mcptools_reembed_project_test.go uses freshToolsForRegister/callRegister harness and asserts JSON envelope shape. tools_test canonical-list green at 30 tools.

## Work summary
Registered reembed_project after delete_project. handleReembedProject mirrors handleDeleteProject. Tool count bumped in three places: main.go totalTools 30, tools.go doc, tools_test.go expectedTools.

## Learnings
Pure pattern-mirror ticket against handleDeleteProject. The three-place lockstep tool-count convention is well-established; the canonical-list test catches drift loud.
