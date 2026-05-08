package mcptools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestReembedProject_RoundTrip exercises the reembed_project MCP tool through
// the same direct-handler harness used by register_agent_test.go: register an
// agent against a real project on disk, then invoke handleReembedProject and
// assert the {"reembed_project": "<slug>", "status": "re-embedding enqueued"}
// envelope. The tool wraps Service.ReembedProject; the underlying behavior
// (sidecar wipe + worker re-enqueue) is covered by projects_reembed_test.go.
func TestReembedProject_RoundTrip(t *testing.T) {
	tools, repo, rec := freshToolsForRegister(t)

	regRes := callRegister(t, tools, map[string]any{
		"model":        "claude-opus-4-7",
		"client_name":  "Claude Code",
		"project_path": repo,
	})
	if regRes.IsError {
		t.Fatalf("register failed: %s", extractText(t, regRes))
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"project_id_or_slug": rec.Slug}
	res, err := tools.handleReembedProject(context.Background(), req)
	if err != nil {
		t.Fatalf("handleReembedProject: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", extractText(t, res))
	}

	body := extractText(t, res)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v (body=%q)", err, body)
	}
	if got["reembed_project"] != rec.Slug {
		t.Errorf("reembed_project: got %v want %q", got["reembed_project"], rec.Slug)
	}
	if got["status"] != "re-embedding enqueued" {
		t.Errorf("status: got %v want %q", got["status"], "re-embedding enqueued")
	}
}
