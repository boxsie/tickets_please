package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// getTicketID drives handleGetTicket with the given ticket_id argument and
// returns the resolved ticket's id (or fails the test).
func getTicketID(t *testing.T, tools *Tools, ticketID string) string {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"ticket_id": ticketID}
	res, err := tools.handleGetTicket(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGetTicket(%q): %v", ticketID, err)
	}
	if res.IsError {
		t.Fatalf("get_ticket(%q) errored: %s", ticketID, extractText(t, res))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(extractText(t, res)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	id, _ := got["id"].(string)
	return id
}

// TestGetTicket_ShortcodeResolution is the acceptance test for ticket 534adaa9:
// get_ticket must return the same ticket whether addressed by UUID, by a
// "<slug>/<number>" shortcode (zero-padded or not), or by a bare number against
// the session-bound project.
func TestGetTicket_ShortcodeResolution(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	// Fresh project "alpha"; first ticket is number 1.
	uuid := seedTicketForCompletion(t, tools, repo)

	for _, ref := range []string{uuid, "alpha/1", "alpha/001", "1"} {
		if got := getTicketID(t, tools, ref); got != uuid {
			t.Errorf("get_ticket(%q) = %q; want %q", ref, got, uuid)
		}
	}
}

// TestGetTicket_BadShortcode_ActionableError verifies a missing shortcode
// surfaces a specific error naming the slug + number, not a generic not-found.
func TestGetTicket_BadShortcode_ActionableError(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	seedTicketForCompletion(t, tools, repo)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"ticket_id": "alpha/9999"}
	res, err := tools.handleGetTicket(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGetTicket: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error for missing shortcode, got success")
	}
	msg := extractText(t, res)
	if !strings.Contains(msg, "9999") || !strings.Contains(msg, "alpha") {
		t.Errorf("error should name slug + number, got: %q", msg)
	}
}
