package mcptools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"

	"tickets_please/internal/config"
	"tickets_please/internal/store"
	"tickets_please/internal/svc"
)

// freshToolsForRegister builds a Tools backed by a real svc.Service rooted in
// a tempdir, plus a fresh Registry. Returns the Tools and an absolute path to
// a separate "repo" dir that contains a valid `.tickets_please/project.yaml`.
func freshToolsForRegister(t *testing.T) (*Tools, string, *store.ProjectRecord) {
	t.Helper()
	cfg := config.Config{
		DataDir:                t.TempDir(),
		DataRoot:               t.TempDir(),
		LockTimeoutSeconds:     5,
		AgentSessionTTLMinutes: 60,
		AgentSessionMaxMinutes: 240,
	}
	s, err := svc.NewWithEmbed(cfg, &fakeEmbed{})
	if err != nil {
		t.Fatalf("svc.NewWithEmbed: %v", err)
	}
	t.Cleanup(s.Close)

	repoDir := t.TempDir()
	tpDir := filepath.Join(repoDir, ".tickets_please")
	if err := os.MkdirAll(tpDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rec := &store.ProjectRecord{
		ID:        uuid.NewString(),
		Slug:      "alpha",
		Name:      "Alpha",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	data, err := store.MarshalYAML(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tpDir, "project.yaml"), data, 0o644); err != nil {
		t.Fatalf("write project.yaml: %v", err)
	}

	tools := NewTools(s, NewRegistry(cfg), nil)
	return tools, repoDir, rec
}

// callRegister builds a CallToolRequest with the supplied args and invokes
// handleRegisterAgent.
func callRegister(t *testing.T, tools *Tools, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := tools.handleRegisterAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("handleRegisterAgent error: %v", err)
	}
	return res
}

func TestRegisterAgent_Happy(t *testing.T) {
	tools, repo, rec := freshToolsForRegister(t)
	res := callRegister(t, tools, map[string]any{
		"model":        "claude-opus-4-7",
		"client_name":  "Claude Code",
		"project_path": repo,
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", extractText(t, res))
	}
	body := extractText(t, res)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["session_id"] != "stdio" {
		t.Errorf("session_id: got %v", got["session_id"])
	}
	if got["agent_id"] == "" || got["agent_id"] == nil {
		t.Errorf("agent_id missing: %v", got)
	}
	if got["project_slug"] != rec.Slug {
		t.Errorf("project_slug: got %v want %v", got["project_slug"], rec.Slug)
	}
	if got["project_path"] != repo {
		t.Errorf("project_path: got %v want %v", got["project_path"], repo)
	}
	if got["expires_at"] == "" || got["expires_at"] == nil {
		t.Errorf("expires_at missing: %v", got)
	}
	// Default agent_key derives from client_name.
	if got["agent_key"] == "" || got["agent_key"] == nil {
		t.Errorf("agent_key missing")
	}
	if got["agent_name"] != "Claude Code" {
		t.Errorf("agent_name default: got %v", got["agent_name"])
	}

	// Registry now holds the session.
	if tools.registry.Len() != 1 {
		t.Errorf("registry Len: got %d want 1", tools.registry.Len())
	}
}

func TestRegisterAgent_MissingProjectPath(t *testing.T) {
	tools, _, _ := freshToolsForRegister(t)
	res := callRegister(t, tools, map[string]any{
		"model":       "claude-opus-4-7",
		"client_name": "Claude Code",
	})
	if !res.IsError {
		t.Fatalf("expected error result, got success: %s", extractText(t, res))
	}
}

func TestRegisterAgent_MissingProjectYAML(t *testing.T) {
	tools, _, _ := freshToolsForRegister(t)
	bare := t.TempDir() // exists but no .tickets_please/project.yaml
	res := callRegister(t, tools, map[string]any{
		"model":        "claude-opus-4-7",
		"client_name":  "Claude Code",
		"project_path": bare,
	})
	if !res.IsError {
		t.Fatalf("expected error, got success: %s", extractText(t, res))
	}
	msg := extractText(t, res)
	if want := "no .tickets_please/project.yaml at " + bare; msg == "" || !contains(msg, want) {
		t.Errorf("expected helpful message containing %q, got %q", want, msg)
	}
}

func TestRegisterAgent_RelativePathRejected(t *testing.T) {
	tools, _, _ := freshToolsForRegister(t)
	res := callRegister(t, tools, map[string]any{
		"model":        "claude-opus-4-7",
		"client_name":  "Claude Code",
		"project_path": "relative/path",
	})
	if !res.IsError {
		t.Fatalf("expected error for relative path")
	}
}

func TestRegisterAgent_Idempotent(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)

	res1 := callRegister(t, tools, map[string]any{
		"model":        "model-A",
		"client_name":  "Claude Code",
		"project_path": repo,
	})
	if res1.IsError {
		t.Fatalf("first register failed: %s", extractText(t, res1))
	}
	res2 := callRegister(t, tools, map[string]any{
		"model":        "model-B",
		"client_name":  "Claude Code",
		"project_path": repo,
	})
	if res2.IsError {
		t.Fatalf("second register failed: %s", extractText(t, res2))
	}
	// Same MCP session id ("stdio" via fallback) → exactly one registry entry.
	if got := tools.registry.Len(); got != 1 {
		t.Errorf("registry Len: got %d want 1 (last-write-wins)", got)
	}
	sess, ok := tools.registry.Get("stdio")
	if !ok {
		t.Fatal("session not in registry")
	}
	if sess.Metadata["model"] != "model-B" {
		t.Errorf("metadata.model: got %q want model-B (last write wins)", sess.Metadata["model"])
	}
}

func TestRegisterAgent_WhoAmIReflectsMetadata(t *testing.T) {
	tools, repo, rec := freshToolsForRegister(t)

	res := callRegister(t, tools, map[string]any{
		"model":          "claude-opus-4-7",
		"model_version":  "20260101",
		"client_name":    "Claude Code",
		"client_version": "1.2.3",
		"project_path":   repo,
	})
	if res.IsError {
		t.Fatalf("register failed: %s", extractText(t, res))
	}

	who, err := tools.handleWhoAmI(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("who_am_i: %v", err)
	}
	body := extractText(t, who)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal who_am_i: %v", err)
	}
	if got["registered"] != true {
		t.Errorf("registered: got %v", got["registered"])
	}
	if got["model"] != "claude-opus-4-7" {
		t.Errorf("model: got %v", got["model"])
	}
	if got["model_version"] != "20260101" {
		t.Errorf("model_version: got %v", got["model_version"])
	}
	if got["client_name"] != "Claude Code" {
		t.Errorf("client_name: got %v", got["client_name"])
	}
	if got["client_version"] != "1.2.3" {
		t.Errorf("client_version: got %v", got["client_version"])
	}
	if got["project_slug"] != rec.Slug {
		t.Errorf("project_slug: got %v want %v", got["project_slug"], rec.Slug)
	}
	if got["project_path"] != repo {
		t.Errorf("project_path: got %v want %v", got["project_path"], repo)
	}
	md, ok := got["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata missing or wrong type: %T", got["metadata"])
	}
	if md["project_path"] != repo {
		t.Errorf("metadata.project_path: got %v", md["project_path"])
	}
}

// contains is a tiny helper to keep the test free of strings.Contains imports
// noise; equivalent to strings.Contains.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// fakeEmbed is a deterministic 768-dim embedding provider used to wire up a
// Service in unit tests without an Ollama sidecar. Returns a constant vector
// (all zeros except index 0 == 1) — the embed worker just needs *something*
// of the right shape; no actual search is exercised here.
type fakeEmbed struct{}

func (f *fakeEmbed) Name() string { return "fake" }
func (f *fakeEmbed) Dim() int     { return 768 }
func (f *fakeEmbed) Embed(_ context.Context, _ string) ([]float32, error) {
	out := make([]float32, 768)
	out[0] = 1
	return out, nil
}
