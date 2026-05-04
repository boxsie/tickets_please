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
	// Cold-start error must point at create_project as the escape valve
	// (no session required — the auth-soft bootstrap path).
	for _, want := range []string{"create_project", "no session required", "register_agent"} {
		if !contains(msg, want) {
			t.Errorf("expected bootstrap-guidance phrase %q in message, got %q", want, msg)
		}
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

// TestWhoAmI_ExpiredField pins the additive `expired` boolean on who_am_i so
// MCP clients can detect a stale session without parsing `expires_at`. Today
// the in-memory Registry entry survives even after the underlying svc-layer
// AgentRecord expires; the field surfaces that gap to the LLM.
func TestWhoAmI_ExpiredField(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	ctx := context.Background()

	res := callRegister(t, tools, map[string]any{
		"model":        "claude-opus-4-7",
		"client_name":  "Claude Code",
		"project_path": repo,
	})
	if res.IsError {
		t.Fatalf("register failed: %s", extractText(t, res))
	}

	// Fresh session: expired must be false.
	who, err := tools.handleWhoAmI(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("who_am_i: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(extractText(t, who)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["expired"] != false {
		t.Errorf("fresh session: expired=%v want false", got["expired"])
	}
	if got["registered"] != true {
		t.Errorf("fresh session: registered=%v want true", got["registered"])
	}

	// Force the cached Session's ExpiresAt into the past. who_am_i reads
	// from the registry (not the AgentStore), so this is the right knob.
	sess, ok := tools.registry.Get("stdio")
	if !ok {
		t.Fatal("session missing")
	}
	sess.ExpiresAt = time.Now().Add(-time.Minute)

	who, err = tools.handleWhoAmI(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("who_am_i (after expiry): %v", err)
	}
	got = map[string]any{}
	if err := json.Unmarshal([]byte(extractText(t, who)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["expired"] != true {
		t.Errorf("expired session: expired=%v want true", got["expired"])
	}
	if got["registered"] != true {
		t.Errorf("expired session: registered=%v want true (semantic = entry exists, not authenticated)",
			got["registered"])
	}
}

// TestWhoAmI_UnregisteredOmitsExpired pins that the un-registered shape is
// unchanged: no `expired` key when there's no expiry to report.
func TestWhoAmI_UnregisteredOmitsExpired(t *testing.T) {
	tools, _, _ := freshToolsForRegister(t)
	// Skip register_agent — registry stays empty so the unregistered branch
	// of handleWhoAmI fires.
	who, err := tools.handleWhoAmI(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("who_am_i: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(extractText(t, who)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["registered"] != false {
		t.Errorf("registered=%v want false", got["registered"])
	}
	if _, present := got["expired"]; present {
		t.Errorf("unregistered response should omit expired field, got %v", got["expired"])
	}
}

// TestCallWithRetry_AutoRefreshOnExpiry exercises the bug from the Codex
// report: a mutating tool call after the underlying svc-layer AgentRecord
// has expired should silently mint a fresh session and complete the original
// call, rather than surfacing "unauthenticated; re-registering..." without
// actually re-registering.
func TestCallWithRetry_AutoRefreshOnExpiry(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	ctx := context.Background()

	// 1. Register, capture initial identity.
	res := callRegister(t, tools, map[string]any{
		"model":        "claude-opus-4-7",
		"client_name":  "Claude Code",
		"project_path": repo,
	})
	if res.IsError {
		t.Fatalf("initial register failed: %s", extractText(t, res))
	}
	prev, ok := tools.registry.Get("stdio")
	if !ok {
		t.Fatal("session missing after register")
	}
	prevAgentID := prev.AgentID
	prevExpiresAt := prev.ExpiresAt

	// 2. Force-expire the AgentRecord on disk so the next requireSession
	//    rejects with ErrUnauthenticated. Bypass the cache by writing
	//    directly through the AgentStore.
	rec, err := tools.svc.AgentStore.ReadAgent(prevAgentID)
	if err != nil {
		t.Fatalf("ReadAgent: %v", err)
	}
	rec.ExpiresAt = time.Now().Add(-time.Minute)
	if err := tools.svc.AgentStore.WriteAgentRecord(rec); err != nil {
		t.Fatalf("WriteAgentRecord: %v", err)
	}

	// 3. Invoke a mutating handler. If auto-refresh works, this succeeds
	//    silently; if not, IsError is true and the response carries the
	//    misleading legacy string.
	createReq := mcp.CallToolRequest{}
	createReq.Params.Arguments = map[string]any{
		"title": "auto-refresh smoke",
		"body":  "ticket created across an expiry boundary",
	}
	createRes, err := tools.handleCreateTicket(ctx, createReq)
	if err != nil {
		t.Fatalf("handleCreateTicket: %v", err)
	}
	if createRes.IsError {
		t.Fatalf("create_ticket failed (auto-refresh did not kick in): %s", extractText(t, createRes))
	}

	// 4. Registry now holds a fresh session under the same MCP session id.
	next, ok := tools.registry.Get("stdio")
	if !ok {
		t.Fatal("session missing after auto-refresh")
	}
	if next.AgentID == prevAgentID {
		t.Errorf("AgentID did not rotate: still %q", next.AgentID)
	}
	if !next.ExpiresAt.After(prevExpiresAt) {
		t.Errorf("ExpiresAt did not advance: prev=%s next=%s", prevExpiresAt, next.ExpiresAt)
	}
	if next.AgentKey != prev.AgentKey {
		t.Errorf("AgentKey changed: prev=%q next=%q (refresh should reuse the cached key)",
			prev.AgentKey, next.AgentKey)
	}
	if next.ProjectSlug != prev.ProjectSlug || next.ProjectPath != prev.ProjectPath {
		t.Errorf("project binding lost across refresh: prev=(%s,%s) next=(%s,%s)",
			prev.ProjectSlug, prev.ProjectPath, next.ProjectSlug, next.ProjectPath)
	}
}

// TestCallWithRetry_RefreshFailureSurfaces exercises the structured fallback
// path: when refreshSession itself fails, callWithRetry must return a
// wrapped ErrUnauthenticated whose message includes the underlying reason,
// not the misleading legacy string.
func TestCallWithRetry_RefreshFailureSurfaces(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	ctx := context.Background()

	res := callRegister(t, tools, map[string]any{
		"model":        "claude-opus-4-7",
		"client_name":  "Claude Code",
		"project_path": repo,
	})
	if res.IsError {
		t.Fatalf("initial register failed: %s", extractText(t, res))
	}
	sess, ok := tools.registry.Get("stdio")
	if !ok {
		t.Fatal("session missing after register")
	}

	// Expire the underlying record so the first fn call returns
	// ErrUnauthenticated and triggers the refresh path.
	rec, err := tools.svc.AgentStore.ReadAgent(sess.AgentID)
	if err != nil {
		t.Fatalf("ReadAgent: %v", err)
	}
	rec.ExpiresAt = time.Now().Add(-time.Minute)
	if err := tools.svc.AgentStore.WriteAgentRecord(rec); err != nil {
		t.Fatalf("WriteAgentRecord: %v", err)
	}

	// Sabotage the cached identity so refreshSession's RegisterAgent fails
	// with ErrInvalidArgument ("agent key required").
	sess.AgentKey = ""

	createReq := mcp.CallToolRequest{}
	createReq.Params.Arguments = map[string]any{"title": "should fail"}
	createRes, err := tools.handleCreateTicket(ctx, createReq)
	if err != nil {
		t.Fatalf("handleCreateTicket: %v", err)
	}
	if !createRes.IsError {
		t.Fatalf("expected error result, got success: %s", extractText(t, createRes))
	}
	msg := extractText(t, createRes)
	if !contains(msg, "unauthenticated:") {
		t.Errorf("error missing structured prefix: %q", msg)
	}
	if !contains(msg, "auto-refresh failed") {
		t.Errorf("error missing auto-refresh-failed signal: %q", msg)
	}
	if contains(msg, "re-registering...") {
		t.Errorf("legacy misleading string still surfaces: %q", msg)
	}
}

// TestCallWithRetry_NoSessionMessage exercises the cold-start error path: when
// callWithRetry runs without a registered session, the returned message must
// point at create_project as the bootstrap escape valve (no session required)
// so an agent can recover from the error text alone.
func TestCallWithRetry_NoSessionMessage(t *testing.T) {
	tools, _, _ := freshToolsForRegister(t)
	// No callRegister. Use any handler that flows through callWithRetry —
	// handleListProjects is the simplest.
	res, err := tools.handleListProjects(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleListProjects: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error result for unregistered session, got: %s", extractText(t, res))
	}
	msg := extractText(t, res)
	if !contains(msg, "unauthenticated:") {
		t.Errorf("missing structured prefix: %q", msg)
	}
	for _, want := range []string{"register_agent", "create_project", "no session required"} {
		if !contains(msg, want) {
			t.Errorf("expected bootstrap-guidance phrase %q in message, got %q", want, msg)
		}
	}
}

// TestCreateProject_NoSessionSucceeds covers the auth-soft bootstrap path at
// the MCP layer: handleCreateProject must succeed without a registered session
// and emit a project record with no created_by. It also exercises the
// project_path bootstrap parameter — without project_path the server has no
// idea where to write, so it's required.
func TestCreateProject_NoSessionSucceeds(t *testing.T) {
	tools, _, _ := freshToolsForRegister(t)

	repoDir := t.TempDir() // empty dir, no .tickets_please/ yet
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"slug":         "bootstrap-smoke",
		"name":         "Bootstrap Smoke",
		"project_path": repoDir,
		"summary":      "A test project created via MCP without a registered session, exercising the auth-soft bootstrap path that breaks the chicken-and-egg between create_project and register_agent. The project should land with created_by empty.",
	}
	res, err := tools.handleCreateProject(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCreateProject: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success without session, got error: %s", extractText(t, res))
	}
	body := extractText(t, res)
	if !contains(body, `"slug":"bootstrap-smoke"`) {
		t.Errorf("expected slug in response, got: %s", body)
	}
	if contains(body, `"created_by":{`) {
		t.Errorf("expected created_by to be omitted/null without session, got: %s", body)
	}
	// project.yaml landed under the explicit repo path, not the service's
	// default DataDir.
	wantPath := filepath.Join(repoDir, ".tickets_please", "project.yaml")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected project.yaml at %s, got: %v", wantPath, err)
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
