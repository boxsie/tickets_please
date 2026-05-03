package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
)

// TestResolveProjectSlug_ExplicitParam: explicit param wins over any session
// default.
func TestResolveProjectSlug_ExplicitParam(t *testing.T) {
	reg := NewRegistry(config.Config{})
	_ = reg.Register("stdio", &Session{ProjectSlug: "session-slug"})
	tools := NewTools(nil, reg, nil)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"project_id_or_slug": "explicit"}

	got, err := tools.resolveProjectSlug(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "explicit" {
		t.Errorf("got %q want %q", got, "explicit")
	}
}

// TestResolveProjectSlug_SessionFallback: no param + session has slug → use it.
func TestResolveProjectSlug_SessionFallback(t *testing.T) {
	reg := NewRegistry(config.Config{})
	_ = reg.Register("stdio", &Session{ProjectSlug: "session-slug"})
	tools := NewTools(nil, reg, nil)

	got, err := tools.resolveProjectSlug(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "session-slug" {
		t.Errorf("got %q want %q", got, "session-slug")
	}
}

// TestResolveProjectSlug_Errors: no param + no session slug → helpful error
// pointing at register_agent.
func TestResolveProjectSlug_Errors(t *testing.T) {
	t.Run("session has no slug", func(t *testing.T) {
		reg := NewRegistry(config.Config{})
		_ = reg.Register("stdio", &Session{})
		tools := NewTools(nil, reg, nil)
		_, err := tools.resolveProjectSlug(context.Background(), mcp.CallToolRequest{})
		if err == nil {
			t.Fatalf("expected error")
		}
		if !strings.Contains(err.Error(), "register_agent") {
			t.Errorf("expected register_agent hint in error, got %q", err.Error())
		}
		if !strings.Contains(err.Error(), "project_id_or_slug") {
			t.Errorf("expected project_id_or_slug hint in error, got %q", err.Error())
		}
	})
	t.Run("no session at all", func(t *testing.T) {
		tools := NewTools(nil, NewRegistry(config.Config{}), nil)
		_, err := tools.resolveProjectSlug(context.Background(), mcp.CallToolRequest{})
		if err == nil {
			t.Fatalf("expected error")
		}
	})
}

// TestListTickets_UsesSessionProjectSlug end-to-end:
//   - register a session with ProjectSlug="alpha"
//   - create the project + a ticket via svc
//   - call list_tickets WITHOUT project_id_or_slug
//   - verify the response contains the ticket.
func TestListTickets_UsesSessionProjectSlug(t *testing.T) {
	tools, repo, rec := freshToolsForRegister(t)
	_ = repo

	// Bind session to the project (mirrors what register_agent would do).
	sess := &Session{
		AgentKey:    "test:abc",
		AgentName:   "Tester",
		ProjectSlug: rec.Slug,
		ProjectPath: repo,
	}
	agentID, expiresAt, err := tools.svc.RegisterAgent(context.Background(), sess.AgentKey, sess.AgentName, nil, 0)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	sess.AgentID = agentID
	sess.ExpiresAt = expiresAt
	if err := tools.registry.Register("stdio", sess); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Create project + ticket via svc, threaded through the agent's session ctx.
	ctx := svc.WithSessionID(context.Background(), sess.AgentID)
	if _, err := tools.svc.CreateProject(ctx, rec.Slug, "Alpha", "", strings.Repeat("a", 250)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := tools.svc.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: rec.Slug,
		Title:           "ticket from session-bound list",
		Body:            "body",
	}); err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	// Now invoke list_tickets with no project_id_or_slug — helper should fill in.
	res, err := tools.handleListTickets(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleListTickets: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", extractText(t, res))
	}
	body := extractText(t, res)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tix, ok := got["tickets"].([]any)
	if !ok || len(tix) == 0 {
		t.Fatalf("expected at least one ticket, got %v", got["tickets"])
	}
	first := tix[0].(map[string]any)
	if first["title"] != "ticket from session-bound list" {
		t.Errorf("title: got %v", first["title"])
	}
}

// TestListTickets_NoSessionNoParam_Errors: list_tickets with neither session
// project nor explicit param returns the helpful error.
func TestListTickets_NoSessionNoParam_Errors(t *testing.T) {
	tools, _, _ := freshToolsForRegister(t)
	// Register a session with no project bound.
	if err := tools.registry.Register("stdio", &Session{
		AgentID:   "agent-x",
		AgentKey:  "test:x",
		AgentName: "Tester",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res, err := tools.handleListTickets(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleListTickets: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error result, got success: %s", extractText(t, res))
	}
	msg := extractText(t, res)
	if !strings.Contains(msg, "register_agent") {
		t.Errorf("expected register_agent hint, got %q", msg)
	}
}
