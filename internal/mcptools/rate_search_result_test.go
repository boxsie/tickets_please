package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// callRate invokes rate_search_result with the given args and returns the
// decoded response payload (updated + rejected).
func callRate(t *testing.T, tools *Tools, args map[string]any) (updated, rejected []map[string]any, raw string) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := tools.handleRateSearchResult(context.Background(), req)
	if err != nil {
		t.Fatalf("handleRateSearchResult: %v", err)
	}
	raw = extractText(t, res)
	if res.IsError {
		return nil, nil, raw
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode response %q: %v", raw, err)
	}
	for _, u := range decoded["updated"].([]any) {
		updated = append(updated, u.(map[string]any))
	}
	for _, r := range decoded["rejected"].([]any) {
		rejected = append(rejected, r.(map[string]any))
	}
	return updated, rejected, raw
}

// seedRegisteredTicket bootstraps a session + creates one ticket, returning
// its id. Mirrors complete_ticket's seedTicketForCompletion but standalone so
// it survives renames in the sibling file.
func seedRegisteredTicket(t *testing.T, tools *Tools, repo string) string {
	t.Helper()
	if res := callRegister(t, tools, map[string]any{
		"model":        "claude-opus-4-7",
		"client_name":  "Claude Code",
		"project_path": repo,
	}); res.IsError {
		t.Fatalf("register failed: %s", extractText(t, res))
	}
	createReq := mcp.CallToolRequest{}
	createReq.Params.Arguments = map[string]any{
		"title": "ticket for rate_search_result",
		"body":  "seed body for rating test",
	}
	createRes, err := tools.handleCreateTicket(context.Background(), createReq)
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

func TestRateSearchResult_HappyPath_LikeThenDislike(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedRegisteredTicket(t, tools, repo)

	key := "ticket:" + id
	updated, rejected, _ := callRate(t, tools, map[string]any{
		"entry_keys": []any{key},
		"rating":     "like",
		"reason":     "useful",
	})
	if len(rejected) != 0 {
		t.Fatalf("unexpected rejected: %v", rejected)
	}
	if len(updated) != 1 {
		t.Fatalf("updated len = %d, want 1", len(updated))
	}
	if updated[0]["entry_key"] != key {
		t.Errorf("entry_key = %v, want %v", updated[0]["entry_key"], key)
	}
	if updated[0]["likes"].(float64) != 1 || updated[0]["dislikes"].(float64) != 0 {
		t.Errorf("counters = %v / %v, want 1 / 0", updated[0]["likes"], updated[0]["dislikes"])
	}

	// Dislike the same key — counters bump independently.
	updated, _, _ = callRate(t, tools, map[string]any{
		"entry_keys": []any{key},
		"rating":     "dislike",
	})
	if updated[0]["likes"].(float64) != 1 || updated[0]["dislikes"].(float64) != 1 {
		t.Errorf("post-dislike counters = %v / %v, want 1 / 1", updated[0]["likes"], updated[0]["dislikes"])
	}
}

func TestRateSearchResult_PartialSuccess(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedRegisteredTicket(t, tools, repo)

	updated, rejected, _ := callRate(t, tools, map[string]any{
		"entry_keys": []any{
			"ticket:" + id,                 // valid
			"learning:" + id,               // valid (learning shares ticket id)
			"ticket:nonexistent-ticket-id", // unknown
			"weird-malformed-key",          // malformed
		},
		"rating": "like",
	})
	if len(updated) != 2 {
		t.Fatalf("updated len = %d, want 2 (got %v)", len(updated), updated)
	}
	if len(rejected) != 2 {
		t.Fatalf("rejected len = %d, want 2 (got %v)", len(rejected), rejected)
	}
	for _, r := range rejected {
		if r["error"] == "" || r["error"] == nil {
			t.Errorf("rejected entry missing error: %v", r)
		}
	}
}

func TestRateSearchResult_InvalidRating(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedRegisteredTicket(t, tools, repo)
	_, _, raw := callRate(t, tools, map[string]any{
		"entry_keys": []any{"ticket:" + id},
		"rating":     "meh",
	})
	if !strings.Contains(raw, "rating must be") && !strings.Contains(raw, "invalid argument") {
		t.Errorf("expected rating-must-be error, got %q", raw)
	}
}

func TestRateSearchResult_EmptyEntryKeys(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	_ = seedRegisteredTicket(t, tools, repo)
	_, _, raw := callRate(t, tools, map[string]any{
		"entry_keys": []any{},
		"rating":     "like",
	})
	if !strings.Contains(raw, "non-empty") {
		t.Errorf("expected non-empty error, got %q", raw)
	}
}

func TestRateSearchResult_ConcurrentRatesAccumulate(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedRegisteredTicket(t, tools, repo)
	key := "ticket:" + id

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			req := mcp.CallToolRequest{}
			req.Params.Arguments = map[string]any{
				"entry_keys": []any{key},
				"rating":     "like",
			}
			res, err := tools.handleRateSearchResult(context.Background(), req)
			if err != nil || res.IsError {
				t.Errorf("concurrent rate failed: err=%v isErr=%v", err, res.IsError)
			}
		}()
	}
	wg.Wait()

	// One final read to assert the total.
	updated, _, _ := callRate(t, tools, map[string]any{
		"entry_keys": []any{key},
		"rating":     "like",
	})
	got := int(updated[0]["likes"].(float64))
	if got != N+1 {
		t.Errorf("Likes = %d after %d concurrent + 1 sequential, want %d", got, N, N+1)
	}
}

func TestRateSearchResult_ReasonCappedTruncated(t *testing.T) {
	tools, repo, _ := freshToolsForRegister(t)
	id := seedRegisteredTicket(t, tools, repo)
	longReason := strings.Repeat("y", 1000)
	updated, rejected, _ := callRate(t, tools, map[string]any{
		"entry_keys": []any{"ticket:" + id},
		"rating":     "like",
		"reason":     longReason,
	})
	if len(rejected) != 0 {
		t.Fatalf("rejected = %v", rejected)
	}
	if len(updated) != 1 {
		t.Fatalf("updated len = %d", len(updated))
	}
	// The exact stored reason isn't visible via the tool response, but the
	// call should not have failed even though reason > 500 chars (truncated
	// silently to keep feedback friction-free).
}

func TestRateSearchResult_BulkArrayEnvelope_JSONRawMessage(t *testing.T) {
	// Mirrors the complete_ticket json.RawMessage envelope test (5688ace3) for
	// the new tool. Wrap a real JSON payload in json.RawMessage and confirm
	// BindArguments recovers the entry_keys array — the misleading-error class
	// shouldn't regress here.
	tools, repo, _ := freshToolsForRegister(t)
	id := seedRegisteredTicket(t, tools, repo)

	body := []byte(`{"entry_keys":["ticket:` + id + `"],"rating":"like","reason":"raw"}`)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = json.RawMessage(body)
	res, err := tools.handleRateSearchResult(context.Background(), req)
	if err != nil {
		t.Fatalf("handleRateSearchResult: %v", err)
	}
	if res.IsError {
		t.Fatalf("rate_search_result rejected json.RawMessage envelope: %s", extractText(t, res))
	}
}
