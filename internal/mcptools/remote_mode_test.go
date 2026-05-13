package mcptools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"tickets_please/internal/config"
	"tickets_please/internal/svc"
)

// freshRemoteTools builds a Tools wired in remote mode (Remote=true) with a
// real svc.Service whose RemoteProjectRoot points at a tempdir. The returned
// path is the configured root — projects land at <root>/<slug>.
func freshRemoteTools(t *testing.T) (*Tools, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		DataDir:                t.TempDir(),
		DataRoot:               t.TempDir(),
		RemoteProjectRoot:      root,
		LockTimeoutSeconds:     5,
		AgentSessionTTLMinutes: 60,
		AgentSessionMaxMinutes: 240,
	}
	s, err := svc.NewWithEmbed(cfg, &fakeEmbed{})
	if err != nil {
		t.Fatalf("svc.NewWithEmbed: %v", err)
	}
	t.Cleanup(s.Close)
	tools := NewTools(s, NewRegistry(cfg), nil)
	tools.Remote = true
	return tools, root
}

// TestCreateProject_RemoteModeNoPath covers the headline change: a remote
// (HTTP) client may omit project_path entirely. The server materialises the
// project at <remote_project_root>/<slug> automatically.
func TestCreateProject_RemoteModeNoPath(t *testing.T) {
	tools, root := freshRemoteTools(t)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"slug":    "remote-auto",
		"name":    "Remote Auto",
		"summary": "Smoke test for remote-mode create_project that drops the explicit project_path argument and lets the server pick <remote_project_root>/<slug> as the materialisation target. Long enough to satisfy the 200-char summary floor.",
	}
	res, err := tools.handleCreateProject(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCreateProject: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", extractText(t, res))
	}

	wantPath := filepath.Join(root, "remote-auto", ".tickets_please", "project.yaml")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected project.yaml at %s, got: %v", wantPath, err)
	}
}

// TestCreateProject_StdioModeRequiresPath pins that local (stdio) clients
// still get a clear error when they forget project_path — they can't fall
// back to a server-side root because there isn't one in the typical local
// dogfood case, and silently writing under DataDir would surprise the user.
func TestCreateProject_StdioModeRequiresPath(t *testing.T) {
	tools, _, _ := freshToolsForRegister(t) // Remote=false by default
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"slug":    "needs-path",
		"name":    "Needs Path",
		"summary": "Stdio clients must still pass project_path because that's how they tell the server where their local repo actually lives — long enough for the 200-char summary floor to be happy here too.",
	}
	res, err := tools.handleCreateProject(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCreateProject: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error for stdio without project_path, got success: %s", extractText(t, res))
	}
	if msg := extractText(t, res); !contains(msg, "project_path") {
		t.Errorf("expected error to mention project_path, got: %s", msg)
	}
}

// TestRegisterAgent_RemoteModeSlugOnly chains create_project (remote, no
// path) → register_agent (remote, slug only). The session must bind cleanly
// without the LLM ever naming a filesystem path.
func TestRegisterAgent_RemoteModeSlugOnly(t *testing.T) {
	tools, root := freshRemoteTools(t)

	createReq := mcp.CallToolRequest{}
	createReq.Params.Arguments = map[string]any{
		"slug":    "slug-only",
		"name":    "Slug Only",
		"summary": "End-to-end remote-mode smoke: server picks the save location, then register_agent binds via project_slug without the LLM having to know any filesystem layout. Padding to clear the 200-char summary floor.",
	}
	createRes, err := tools.handleCreateProject(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create_project: %v", err)
	}
	if createRes.IsError {
		t.Fatalf("create_project errored: %s", extractText(t, createRes))
	}

	regReq := mcp.CallToolRequest{}
	regReq.Params.Arguments = map[string]any{
		"model":        "claude-opus-4-7",
		"client_name":  "Claude Code",
		"project_slug": "slug-only",
	}
	regRes, err := tools.handleRegisterAgent(context.Background(), regReq)
	if err != nil {
		t.Fatalf("register_agent: %v", err)
	}
	if regRes.IsError {
		t.Fatalf("register_agent errored: %s", extractText(t, regRes))
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(extractText(t, regRes)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["project_slug"] != "slug-only" {
		t.Errorf("project_slug: got %v want slug-only", got["project_slug"])
	}
	wantPath := filepath.Join(root, "slug-only")
	if got["project_path"] != wantPath {
		t.Errorf("project_path: got %v want %v", got["project_path"], wantPath)
	}
}

// TestRegisterAgent_NeitherSlugNorPath pins that the new shape still demands
// *something* identifying the project. Without project_path or project_slug
// there is nothing for the server to bind to.
func TestRegisterAgent_NeitherSlugNorPath(t *testing.T) {
	tools, _ := freshRemoteTools(t)
	regReq := mcp.CallToolRequest{}
	regReq.Params.Arguments = map[string]any{
		"model":       "claude-opus-4-7",
		"client_name": "Claude Code",
	}
	res, err := tools.handleRegisterAgent(context.Background(), regReq)
	if err != nil {
		t.Fatalf("handleRegisterAgent: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error, got success: %s", extractText(t, res))
	}
	if msg := extractText(t, res); !contains(msg, "project_slug") || !contains(msg, "project_path") {
		t.Errorf("expected error to mention both project_slug and project_path, got: %s", msg)
	}
}
