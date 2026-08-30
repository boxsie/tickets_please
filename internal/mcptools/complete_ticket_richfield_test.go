package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// richCompletionField is a long, multi-paragraph markdown payload exercising
// the constructs the bug report fingered as triggers: fenced code blocks,
// nested/bulleted lists, bold spans, headings, and non-ASCII text. Each of the
// three completion fields gets a value built from this so the test mirrors a
// realistic "be thorough" completion rather than a short plain string.
const richCompletionField = `Completed the work across **several** subsystems. Summary follows.

### What changed

- Reworked the decode path so arguments survive a non-map envelope:
  - ` + "`map[string]any`" + ` fast path preserved
  - ` + "`json.RawMessage`" + ` fallback added
- Tightened the error message so it names the *actual* missing field.

` + "```go" + `
func requireStringArgs(req mcp.CallToolRequest, keys ...string) (map[string]string, error) {
    raw := req.GetArguments()
    if raw == nil { /* recover via BindArguments */ }
    return out, nil
}
` + "```" + `

Notes on edge cases — déjà vu, naïve façade, 你好, emoji 🎟️ — all must round-trip
without mangling. Paragraph two deliberately runs long so the total payload is
comfortably over a kilobyte and spans blank-line-separated blocks, which is the
shape that was reported to fail with a misleading "required argument not found".`

// seedTicketForCompletion registers a session and creates a ticket, returning
// its id. It reuses the freshToolsForRegister harness.
func seedTicketForCompletion(t *testing.T, tools *Tools, repo string) string {
	t.Helper()
	ctx := context.Background()
	if res := callRegister(t, tools, map[string]any{
		"model":        "claude-opus-4-7",
		"client_name":  "Claude Code",
		"project_path": repo,
	}); res.IsError {
		t.Fatalf("register failed: %s", extractText(t, res))
	}
	createReq := mcp.CallToolRequest{}
	createReq.Params.Arguments = map[string]any{
		"title": "ticket to complete with rich fields",
		"body":  "seed",
	}
	createRes, err := tools.handleCreateTicket(ctx, createReq)
	if err != nil {
		t.Fatalf("handleCreateTicket: %v", err)
	}
	if createRes.IsError {
		t.Fatalf("create_ticket failed: %s", extractText(t, createRes))
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(extractText(t, createRes)), &created); err != nil {
		t.Fatalf("unmarshal created ticket: %v", err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created ticket has no id: %v", created)
	}
	return id
}

// TestCompleteTicket_LongRichFields_MapEnvelope is the primary regression for
// ticket 5688ace3: long, richly-formatted markdown in all three completion
// fields must complete the ticket, not fail with a misleading "required
// argument work_summary not found".
func TestCompleteTicket_LongRichFields_MapEnvelope(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedTicketForCompletion(t, tools, repo)

	te := "Testing evidence:\n\n" + richCompletionField
	ws := "Work summary:\n\n" + richCompletionField
	ln := "Learnings:\n\n" + richCompletionField

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"ticket_id":        id,
		"testing_evidence": te,
		"work_summary":     ws,
		"learnings":        ln,
	}
	res, err := tools.handleCompleteTicket(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCompleteTicket: %v", err)
	}
	if res.IsError {
		t.Fatalf("complete_ticket rejected long/rich fields: %s", extractText(t, res))
	}

	var done map[string]any
	if err := json.Unmarshal([]byte(extractText(t, res)), &done); err != nil {
		t.Fatalf("unmarshal completed ticket: %v", err)
	}
	if done["column"] != "done" {
		t.Fatalf("ticket not done: column=%v", done["column"])
	}
	// The rich content must survive the round-trip (code fence + unicode).
	if ev, _ := done["testing_evidence"].(string); !strings.Contains(ev, "🎟️") || !strings.Contains(ev, "```go") {
		t.Errorf("testing_evidence lost content on round-trip: %q", ev)
	}
}

// TestCompleteTicket_RawMessageEnvelope drives the handler with arguments
// delivered as json.RawMessage rather than a pre-decoded map[string]any. This
// is the envelope shape that made GetArguments() return nil and RequireString
// emit "required argument not found" for fields that were actually present —
// the root cause behind the misleading error. requireStringArgs must recover
// it via BindArguments and complete successfully.
func TestCompleteTicket_RawMessageEnvelope(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedTicketForCompletion(t, tools, repo)

	payload, err := json.Marshal(map[string]any{
		"ticket_id":        id,
		"testing_evidence": "raw-envelope " + richCompletionField,
		"work_summary":     "raw-envelope " + richCompletionField,
		"learnings":        "raw-envelope " + richCompletionField,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = json.RawMessage(payload)
	res, err := tools.handleCompleteTicket(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCompleteTicket: %v", err)
	}
	if res.IsError {
		t.Fatalf("complete_ticket failed on json.RawMessage envelope: %s", extractText(t, res))
	}
}

// TestCompleteTicket_MissingLearningsAccurateError verifies the error message
// is honest under the relaxed schema: only `learnings` is required, so
// omitting it (even with the optional fields supplied) surfaces a clear
// `learnings` error — never the misleading multi-field message that the
// json.RawMessage envelope bug used to produce.
func TestCompleteTicket_MissingLearningsAccurateError(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedTicketForCompletion(t, tools, repo)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"ticket_id":        id,
		"testing_evidence": "only this one is supplied, and it is long enough",
		"work_summary":     "and this one is also supplied",
		// learnings deliberately omitted.
	}
	res, err := tools.handleCompleteTicket(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCompleteTicket: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error for missing learnings, got success")
	}
	msg := extractText(t, res)
	if !strings.Contains(msg, "learnings") {
		t.Errorf("error should name the missing `learnings` field, got: %q", msg)
	}
}

// TestCompleteTicket_LearningsOnlyEnvelope verifies the handler accepts a
// payload containing only the required fields (ticket_id + learnings) — the
// optional testing_evidence and work_summary may be omitted entirely.
func TestCompleteTicket_LearningsOnlyEnvelope(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedTicketForCompletion(t, tools, repo)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"ticket_id": id,
		"learnings": "small change so audit-trail fields were omitted intentionally",
	}
	res, err := tools.handleCompleteTicket(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCompleteTicket: %v", err)
	}
	if res.IsError {
		t.Fatalf("complete_ticket rejected learnings-only payload: %s", extractText(t, res))
	}
	var done map[string]any
	if err := json.Unmarshal([]byte(extractText(t, res)), &done); err != nil {
		t.Fatalf("unmarshal completed ticket: %v", err)
	}
	if done["column"] != "done" {
		t.Fatalf("ticket not done: column=%v", done["column"])
	}
	if ev, _ := done["testing_evidence"].(string); ev != "" {
		t.Errorf("testing_evidence should be empty on learnings-only completion, got %q", ev)
	}
	if ws, _ := done["work_summary"].(string); ws != "" {
		t.Errorf("work_summary should be empty on learnings-only completion, got %q", ws)
	}
}
