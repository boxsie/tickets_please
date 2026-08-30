package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// richBody is a long, multi-paragraph markdown payload exercising the
// constructs reported to fail when set via the MCP tools: fenced code blocks,
// nested/bulleted lists, bold spans, headings, and non-ASCII text. It is
// deliberately over a kilobyte and spans blank-line-separated blocks — the
// shape that, delivered as a json.RawMessage envelope, made GetArguments()
// return nil and RequireString emit "required argument not found" for fields
// that were actually present. (Shares the spirit of richCompletionField in
// complete_ticket_richfield_test.go but covers the body/comment set path.)
const richBody = `This ticket body spans **several** blocks to exercise the decode path.

### Background

- The first instinct is to set a rich body:
  - with ` + "`inline code`" + ` and **bold**
  - and ` + "`json.RawMessage`" + ` envelopes
- The body must survive the MCP arg-decode boundary unmangled.

` + "```go" + `
func handleCreateTicket(req mcp.CallToolRequest) {
    required, _ := requireStringArgs(req, "title")
    args, _ := decodeArgEnvelope(req)
    _ = args["body"]
}
` + "```" + `

Edge cases — déjà vu, naïve façade, 你好, emoji 🎟️ — must round-trip without
mangling. This paragraph runs long on purpose so the total payload comfortably
exceeds a kilobyte and forces the non-map envelope path that the bug report
fingered as the failure mode.`

// rawReq builds a CallToolRequest whose arguments arrive as a json.RawMessage
// rather than a pre-decoded map[string]any — the envelope shape that defeats
// req.RequireString / req.GetArguments and is the whole point of these tests.
func rawReq(t *testing.T, args map[string]any) mcp.CallToolRequest {
	t.Helper()
	payload, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = json.RawMessage(payload)
	return req
}

func registerForRichBody(t *testing.T, tools *Tools, repo string) {
	t.Helper()
	if res := callRegister(t, tools, map[string]any{
		"model":        "claude-opus-4-7",
		"client_name":  "Claude Code",
		"project_path": repo,
	}); res.IsError {
		t.Fatalf("register failed: %s", extractText(t, res))
	}
}

// assertRichRoundTrip checks the rich body survived: code fence + unicode emoji.
func assertRichRoundTrip(t *testing.T, got string) {
	t.Helper()
	if !strings.Contains(got, "🎟️") || !strings.Contains(got, "```go") {
		t.Errorf("rich content lost on round-trip: %q", got)
	}
}

// TestCreateTicket_RawMessageEnvelope_RichBody is the primary regression for
// ticket 3d6cbfd7: a rich markdown body delivered as json.RawMessage must
// create the ticket and round-trip the body intact, not fail with the
// misleading "required argument title not found".
func TestCreateTicket_RawMessageEnvelope_RichBody(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	registerForRichBody(t, tools, repo)

	req := rawReq(t, map[string]any{
		"title": "rich body via raw envelope",
		"body":  richBody,
	})
	res, err := tools.handleCreateTicket(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCreateTicket: %v", err)
	}
	if res.IsError {
		t.Fatalf("create_ticket rejected raw-envelope rich body: %s", extractText(t, res))
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(extractText(t, res)), &created); err != nil {
		t.Fatalf("unmarshal created ticket: %v", err)
	}
	body, _ := created["body"].(string)
	assertRichRoundTrip(t, body)
}

// TestUpdateTicket_RawMessageEnvelope_RichBody covers the update path: edit an
// existing ticket's body with a rich markdown payload via json.RawMessage.
func TestUpdateTicket_RawMessageEnvelope_RichBody(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedTicketForCompletion(t, tools, repo)

	req := rawReq(t, map[string]any{
		"ticket_id": id,
		"body":      richBody,
	})
	res, err := tools.handleUpdateTicket(context.Background(), req)
	if err != nil {
		t.Fatalf("handleUpdateTicket: %v", err)
	}
	if res.IsError {
		t.Fatalf("update_ticket rejected raw-envelope rich body: %s", extractText(t, res))
	}
	var updated map[string]any
	if err := json.Unmarshal([]byte(extractText(t, res)), &updated); err != nil {
		t.Fatalf("unmarshal updated ticket: %v", err)
	}
	body, _ := updated["body"].(string)
	assertRichRoundTrip(t, body)
}

func TestUpdateTicket_RawMessageEnvelope_DependencyLists(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	registerForRichBody(t, tools, repo)
	ctx := context.Background()
	create := func(title string) string {
		t.Helper()
		req := mcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{"title": title}
		res, err := tools.handleCreateTicket(ctx, req)
		if err != nil {
			t.Fatalf("handleCreateTicket: %v", err)
		}
		if res.IsError {
			t.Fatalf("create_ticket failed: %s", extractText(t, res))
		}
		var created map[string]any
		if err := json.Unmarshal([]byte(extractText(t, res)), &created); err != nil {
			t.Fatalf("unmarshal created ticket: %v", err)
		}
		id, _ := created["id"].(string)
		if id == "" {
			t.Fatalf("created ticket has no id: %v", created)
		}
		return id
	}
	upstream := create("upstream")
	parallel := create("parallel")
	child := create("child")

	req := rawReq(t, map[string]any{
		"ticket_id":           child,
		"depends_on":          []string{upstream},
		"parallelizable_with": []string{parallel},
	})
	res, err := tools.handleUpdateTicket(ctx, req)
	if err != nil {
		t.Fatalf("handleUpdateTicket: %v", err)
	}
	if res.IsError {
		t.Fatalf("update_ticket rejected dependency lists: %s", extractText(t, res))
	}
	var updated map[string]any
	if err := json.Unmarshal([]byte(extractText(t, res)), &updated); err != nil {
		t.Fatalf("unmarshal updated ticket: %v", err)
	}
	deps, _ := updated["depends_on"].([]any)
	parallelWith, _ := updated["parallelizable_with"].([]any)
	if len(deps) != 1 || deps[0] != upstream {
		t.Fatalf("depends_on = %v, want [%s]", deps, upstream)
	}
	if len(parallelWith) != 1 || parallelWith[0] != parallel {
		t.Fatalf("parallelizable_with = %v, want [%s]", parallelWith, parallel)
	}

	clearReq := rawReq(t, map[string]any{
		"ticket_id":           child,
		"depends_on":          []string{},
		"parallelizable_with": []string{},
	})
	res, err = tools.handleUpdateTicket(ctx, clearReq)
	if err != nil {
		t.Fatalf("handleUpdateTicket clear: %v", err)
	}
	if res.IsError {
		t.Fatalf("update_ticket rejected dependency clear: %s", extractText(t, res))
	}
	if err := json.Unmarshal([]byte(extractText(t, res)), &updated); err != nil {
		t.Fatalf("unmarshal cleared ticket: %v", err)
	}
	deps, _ = updated["depends_on"].([]any)
	parallelWith, _ = updated["parallelizable_with"].([]any)
	if len(deps) != 0 || len(parallelWith) != 0 {
		t.Fatalf("dependency lists not cleared: depends=%v parallel=%v", deps, parallelWith)
	}
}

// TestAddComment_RawMessageEnvelope_RichBody covers the comment set path.
func TestAddComment_RawMessageEnvelope_RichBody(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedTicketForCompletion(t, tools, repo)

	req := rawReq(t, map[string]any{
		"ticket_id": id,
		"body":      richBody,
	})
	res, err := tools.handleAddComment(context.Background(), req)
	if err != nil {
		t.Fatalf("handleAddComment: %v", err)
	}
	if res.IsError {
		t.Fatalf("add_comment rejected raw-envelope rich body: %s", extractText(t, res))
	}
	var c map[string]any
	if err := json.Unmarshal([]byte(extractText(t, res)), &c); err != nil {
		t.Fatalf("unmarshal comment: %v", err)
	}
	body, _ := c["body"].(string)
	assertRichRoundTrip(t, body)
}

// TestMoveTicket_RawMessageEnvelope_RichComment covers the move comment path:
// a long/rich move comment delivered as json.RawMessage must not be reported
// missing.
func TestMoveTicket_RawMessageEnvelope_RichComment(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedTicketForCompletion(t, tools, repo)

	req := rawReq(t, map[string]any{
		"ticket_id":     id,
		"target_column": "in_progress",
		"comment":       richBody,
	})
	res, err := tools.handleMoveTicket(context.Background(), req)
	if err != nil {
		t.Fatalf("handleMoveTicket: %v", err)
	}
	if res.IsError {
		t.Fatalf("move_ticket rejected raw-envelope rich comment: %s", extractText(t, res))
	}
	var moved map[string]any
	if err := json.Unmarshal([]byte(extractText(t, res)), &moved); err != nil {
		t.Fatalf("unmarshal moved ticket: %v", err)
	}
	if moved["column"] != "in_progress" {
		t.Fatalf("ticket not moved: column=%v", moved["column"])
	}
}
