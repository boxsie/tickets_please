package mcptools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"tickets_please/internal/store"
)

// TestRegisterAgent_ActingForWiring covers the MCP-layer plumbing of the
// acting_for_user_id param: a known user is accepted and surfaced on both the
// register response and who_am_i; an unknown user is rejected.
func TestRegisterAgent_ActingForWiring(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)

	// Seed a user the agent can act for.
	if err := tools.svc.UserStore.WriteUser(&store.UserRecord{
		ID:          "u-dan",
		DisplayName: "Dan",
		CreatedAt:   time.Now().UTC(),
		LastLoginAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	res := callRegister(t, tools, map[string]any{
		"model":              "claude-opus-4-7",
		"client_name":        "Claude Code",
		"project_path":       repo,
		"acting_for_user_id": "u-dan",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", extractText(t, res))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(extractText(t, res)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["acting_for_user_id"] != "u-dan" {
		t.Fatalf("register response acting_for_user_id: got %v", got["acting_for_user_id"])
	}

	// who_am_i surfaces the binding.
	who, err := tools.handleWhoAmI(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleWhoAmI: %v", err)
	}
	var whoGot map[string]any
	if err := json.Unmarshal([]byte(extractText(t, who)), &whoGot); err != nil {
		t.Fatalf("unmarshal who_am_i: %v", err)
	}
	if whoGot["acting_for_user_id"] != "u-dan" {
		t.Fatalf("who_am_i acting_for_user_id: got %v", whoGot["acting_for_user_id"])
	}
}

// TestRegisterAgent_ActingForUnknownUserRejected: an acting_for_user_id that
// names no registered user is an invalid-argument error.
func TestRegisterAgent_ActingForUnknownUserRejected(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	res := callRegister(t, tools, map[string]any{
		"model":              "claude-opus-4-7",
		"client_name":        "Claude Code",
		"project_path":       repo,
		"acting_for_user_id": "ghost",
	})
	if !res.IsError {
		t.Fatalf("expected error for unknown acting-for user, got success: %s", extractText(t, res))
	}
}
