package mcptools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestHandleListCommentsScoped_Wiring drives the handler end-to-end: register a
// session, create a ticket, add a user comment + a column move (system_move),
// then confirm the default (exclude_system=true) hides the move and the result
// carries ticket_title + a next_cursor field.
func TestHandleListCommentsScoped_Wiring(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	ctx := context.Background()
	if res := callRegister(t, tools, map[string]any{
		"model": "claude-opus-4-7", "client_name": "Claude Code", "project_path": repo,
	}); res.IsError {
		t.Fatalf("register: %s", extractText(t, res))
	}

	// Create a ticket.
	createReq := mcp.CallToolRequest{}
	createReq.Params.Arguments = map[string]any{"title": "scoped", "body": "b"}
	createRes, err := tools.handleCreateTicket(ctx, createReq)
	if err != nil || createRes.IsError {
		t.Fatalf("create_ticket: %v %v", err, createRes)
	}
	var created map[string]any
	_ = json.Unmarshal([]byte(extractText(t, createRes)), &created)
	id, _ := created["id"].(string)

	// A user comment + a move (the move adds a system_move comment).
	addReq := mcp.CallToolRequest{}
	addReq.Params.Arguments = map[string]any{"ticket_id": id, "body": "a human note"}
	if res, _ := tools.handleAddComment(ctx, addReq); res.IsError {
		t.Fatalf("add_comment: %s", extractText(t, res))
	}
	moveReq := mcp.CallToolRequest{}
	moveReq.Params.Arguments = map[string]any{"ticket_id": id, "target_column": "in_progress", "comment": "go"}
	if res, _ := tools.handleMoveTicket(ctx, moveReq); res.IsError {
		t.Fatalf("move_ticket: %s", extractText(t, res))
	}

	// Default scoped list: exclude_system defaults true → only the user comment.
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{} // project bound via session
	res, err := tools.handleListCommentsScoped(ctx, req)
	if err != nil {
		t.Fatalf("handleListCommentsScoped: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_comments_scoped error: %s", extractText(t, res))
	}
	var out struct {
		Comments []struct {
			Kind        string `json:"kind"`
			TicketTitle string `json:"ticket_title"`
		} `json:"comments"`
		NextCursor any `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(extractText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Comments) != 1 {
		t.Fatalf("default exclude_system: got %d comments, want 1 (the user note)", len(out.Comments))
	}
	if out.Comments[0].Kind != "user" {
		t.Errorf("kind = %q, want user", out.Comments[0].Kind)
	}
	if out.Comments[0].TicketTitle != "scoped" {
		t.Errorf("ticket_title = %q, want \"scoped\"", out.Comments[0].TicketTitle)
	}

	// exclude_system=false surfaces the system_move too.
	req2 := mcp.CallToolRequest{}
	req2.Params.Arguments = map[string]any{"exclude_system": false}
	res2, _ := tools.handleListCommentsScoped(ctx, req2)
	if res2.IsError {
		t.Fatalf("list_comments_scoped (incl system): %s", extractText(t, res2))
	}
	var out2 struct {
		Comments []json.RawMessage `json:"comments"`
	}
	_ = json.Unmarshal([]byte(extractText(t, res2)), &out2)
	if len(out2.Comments) != 2 {
		t.Fatalf("exclude_system=false: got %d comments, want 2", len(out2.Comments))
	}

	// Bad timestamp is a clean argument error, not a panic.
	req3 := mcp.CallToolRequest{}
	req3.Params.Arguments = map[string]any{"since": "not-a-time"}
	res3, _ := tools.handleListCommentsScoped(ctx, req3)
	if !res3.IsError {
		t.Fatal("expected error for malformed since timestamp")
	}
}
