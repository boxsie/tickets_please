package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// expectedTools is the canonical list — keep in sync with SPEC.md and
// RegisterAll. The test fails loud if any name drifts.
var expectedTools = []string{
	// Projects (7)
	"list_projects", "create_project", "get_project", "get_project_summary",
	"load_project", "update_project", "delete_project",
	// Phases (7)
	"list_phases", "create_phase", "get_phase", "get_phase_summary",
	"update_phase", "delete_phase", "list_waves",
	// Tickets (7)
	"list_tickets", "create_ticket", "get_ticket", "update_ticket",
	"move_ticket", "complete_ticket", "assign_ticket_to_phase",
	// Comments (2)
	"add_comment", "list_comments",
	// Search (4)
	"search_projects", "search_tickets", "search_learnings", "search_comments",
	// Introspection (2)
	"who_am_i", "register_agent",
}

// newTestRegistry returns a Registry pre-loaded with a single "stdio" session
// for use in unit tests that don't exercise the actual transport.
func newTestRegistry() *Registry {
	r := NewRegistry(config.Config{})
	_ = r.Register("stdio", &Session{
		AgentID:   "test-agent-id",
		AgentKey:  "test:abc123",
		AgentName: "Tester",
	})
	return r
}

// TestRegisterAllTools spins up an MCP server, registers every tool, and
// verifies all 28 names are present with non-empty descriptions. The svc
// pointer is nil because no handler is invoked here — registration alone
// covers the schema-builder code paths.
func TestRegisterAllTools(t *testing.T) {
	tools := NewTools(nil, newTestRegistry(), nil)
	srv := mcpserver.NewMCPServer("tickets_please_test", "0.0.0")
	tools.RegisterAll(srv)

	registered := srv.ListTools()
	if len(registered) != len(expectedTools) {
		gotNames := sortedNames(registered)
		t.Fatalf("expected %d tools, got %d: %v", len(expectedTools), len(registered), gotNames)
	}
	for _, want := range expectedTools {
		entry, ok := registered[want]
		if !ok {
			t.Errorf("tool %q not registered", want)
			continue
		}
		if entry.Tool.Description == "" {
			t.Errorf("tool %q has empty description", want)
		}
	}

	// Critical descriptions carry the load-bearing instructions for the LLM.
	// If they drift, the system loses its self-feeding behavior. Pin a few.
	mustContain := map[string]string{
		"search_learnings":    "before starting non-trivial work",
		"get_project_summary": "before doing any non-trivial work",
		"create_project":      "≥200 chars",
		"complete_ticket":     "learnings",
		"move_ticket":         "explaining *why*",
	}
	for name, snippet := range mustContain {
		entry := registered[name]
		if entry == nil {
			continue // missing-tool error reported above
		}
		if !strings.Contains(entry.Tool.Description, snippet) {
			t.Errorf("tool %q description missing %q: got %q", name, snippet, entry.Tool.Description)
		}
	}
}

// TestSchemasParse encodes each tool's InputSchema as JSON and re-decodes it
// to a generic map; round-trip success means the schema MarshalJSON path is
// well-formed and properties are serialised. Any malformed schema we
// accidentally wired up (e.g. typo in mcp.WithString) fails here.
func TestSchemasParse(t *testing.T) {
	tools := NewTools(nil, newTestRegistry(), nil)
	srv := mcpserver.NewMCPServer("tickets_please_test", "0.0.0")
	tools.RegisterAll(srv)

	for _, name := range expectedTools {
		entry := srv.GetTool(name)
		if entry == nil {
			t.Errorf("tool %q missing", name)
			continue
		}
		raw, err := json.Marshal(entry.Tool)
		if err != nil {
			t.Errorf("tool %q marshal: %v", name, err)
			continue
		}
		var generic map[string]any
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Errorf("tool %q unmarshal: %v", name, err)
			continue
		}
		if generic["name"] != name {
			t.Errorf("tool %q: roundtrip name mismatch: %v", name, generic["name"])
		}
		if _, ok := generic["inputSchema"]; !ok {
			t.Errorf("tool %q: missing inputSchema after roundtrip", name)
		}
	}
}

// TestWhoAmI directly invokes the who_am_i handler with a pre-registered
// session and verifies the JSON shape carries key/name/session_id/expires_at.
func TestWhoAmI(t *testing.T) {
	exp := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	reg := NewRegistry(config.Config{})
	sess := &Session{
		AgentID:   "sess-uuid-1234",
		AgentKey:  "test:abc123",
		AgentName: "Tester",
		ExpiresAt: exp,
	}
	if err := reg.Register("stdio", sess); err != nil {
		t.Fatalf("Register: %v", err)
	}

	tools := NewTools(nil, reg, nil)
	// No ClientSession in context — sessionIDFromContext falls back to "stdio".
	res, err := tools.handleWhoAmI(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleWhoAmI returned error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	body := extractText(t, res)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal who_am_i: %v (body=%q)", err, body)
	}
	if got["key"] != "test:abc123" {
		t.Errorf("key: got %v", got["key"])
	}
	if got["name"] != "Tester" {
		t.Errorf("name: got %v", got["name"])
	}
	// session_id in the response is the MCP session ID ("stdio"), not the
	// svc-layer agent ID.
	if got["session_id"] != "stdio" {
		t.Errorf("session_id: got %v want \"stdio\"", got["session_id"])
	}
	if got["expires_at"] != exp.Format(time.RFC3339) {
		t.Errorf("expires_at: got %v want %v", got["expires_at"], exp.Format(time.RFC3339))
	}
}

// TestDefaultStdioSession verifies the cfg-derived defaults: empty
// MCPAgentKey/Name fall back to the SPEC strings, and the random suffix is
// included so two MCPs against the same data dir don't collide.
func TestDefaultStdioSession(t *testing.T) {
	sess := DefaultStdioSession(config.Config{})
	if !strings.HasPrefix(sess.AgentKey, "tickets_please_mcp:") {
		t.Errorf("default key %q missing expected prefix", sess.AgentKey)
	}
	if sess.AgentName != "tickets_please_mcp" {
		t.Errorf("default name %q", sess.AgentName)
	}

	sess2 := DefaultStdioSession(config.Config{MCPAgentKey: "claude:abc", MCPAgentName: "Claude"})
	if sess2.AgentKey != "claude:abc" {
		t.Errorf("explicit key dropped: %q", sess2.AgentKey)
	}
	if sess2.AgentName != "Claude" {
		t.Errorf("explicit name dropped: %q", sess2.AgentName)
	}
}

// TestFormatTicket pins the snake_case/string-column shape for a hydrated
// ticket. BlockedBy is preserved; nil pointer fields render as JSON null.
func TestFormatTicket(t *testing.T) {
	now := time.Date(2026, 5, 2, 13, 30, 0, 0, time.UTC)
	phase := "phase-uuid"
	te := "tested foo"
	ws := "did x"
	ln := "watch out for y"
	tk := &domain.Ticket{
		ID:                 "tic-1",
		ProjectID:          "proj-1",
		Title:              "Implement X",
		Body:               "Body text",
		Column:             domain.ColumnInProgress,
		PhaseID:            &phase,
		Wave:               2,
		DependsOn:          []string{"dep-1"},
		ParallelizableWith: []string{"par-1"},
		BlockedBy:          []string{"dep-1"},
		CreatedBy:          &domain.AgentRef{ID: "agent-1", Name: "Agent One"},
		CreatedAt:          now,
		UpdatedAt:          now,
		TestingEvidence:    &te,
		WorkSummary:        &ws,
		Learnings:          &ln,
	}
	got := formatTicket(tk)

	wantKeys := []string{
		"id", "project_id", "title", "body", "column", "phase_id", "wave",
		"depends_on", "parallelizable_with", "blocked_by",
		"created_by", "completed_by", "completed_at",
		"created_at", "updated_at",
		"testing_evidence", "work_summary", "learnings",
	}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	if got["column"] != "in_progress" {
		t.Errorf("column: got %v want \"in_progress\"", got["column"])
	}
	if got["phase_id"] != phase {
		t.Errorf("phase_id: got %v", got["phase_id"])
	}
	if got["completed_at"] != nil {
		t.Errorf("completed_at: want nil for incomplete ticket, got %v", got["completed_at"])
	}
	if got["completed_by"] != nil {
		t.Errorf("completed_by: want nil, got %v", got["completed_by"])
	}
	if cb, ok := got["created_by"].(map[string]any); !ok {
		t.Errorf("created_by: not a map: %v", got["created_by"])
	} else if cb["id"] != "agent-1" || cb["name"] != "Agent One" {
		t.Errorf("created_by: got %v", cb)
	}
	if got["created_at"] != now.Format(time.RFC3339) {
		t.Errorf("created_at: got %v", got["created_at"])
	}

	// Round-trip through JSON to confirm the shape encodes cleanly.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

// TestFormatComment ensures the from_column / to_column rendering is correct
// for both system_move (with both set) and free-form user comments (both nil).
func TestFormatComment(t *testing.T) {
	now := time.Date(2026, 5, 2, 14, 0, 0, 0, time.UTC)
	from := domain.ColumnTodo
	to := domain.ColumnInProgress
	move := &domain.Comment{
		ID:         "c1",
		TicketID:   "t1",
		Kind:       domain.CommentKindSystemMove,
		Body:       "moved\n",
		FromColumn: &from,
		ToColumn:   &to,
		Author:     &domain.AgentRef{ID: "a1", Name: "A"},
		CreatedAt:  now,
	}
	g := formatComment(move)
	if g["from_column"] != "todo" || g["to_column"] != "in_progress" {
		t.Errorf("system_move columns: got from=%v to=%v", g["from_column"], g["to_column"])
	}
	if g["kind"] != "system_move" {
		t.Errorf("kind: got %v", g["kind"])
	}
	if author, ok := g["author"].(map[string]any); !ok || author["id"] != "a1" {
		t.Errorf("author: got %v", g["author"])
	}

	user := &domain.Comment{
		ID:        "c2",
		TicketID:  "t1",
		Kind:      domain.CommentKindUser,
		Body:      "hi",
		CreatedAt: now,
	}
	g2 := formatComment(user)
	if g2["from_column"] != nil {
		t.Errorf("user comment from_column should be nil, got %v", g2["from_column"])
	}
	if g2["to_column"] != nil {
		t.Errorf("user comment to_column should be nil, got %v", g2["to_column"])
	}
}

// TestFormatProject ensures the Project shape carries all the agent ref +
// timestamp keys SPEC requires.
func TestFormatProject(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	p := &domain.Project{
		ID:          "proj-1",
		Slug:        "demo",
		Name:        "Demo",
		Description: "A demo",
		Summary:     "long summary",
		CreatedBy:   &domain.AgentRef{ID: "a1", Name: "A"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	g := formatProject(p)
	if g["id"] != "proj-1" || g["slug"] != "demo" || g["name"] != "Demo" {
		t.Errorf("scalar fields: %v", g)
	}
	if g["created_at"] != now.Format(time.RFC3339) {
		t.Errorf("created_at: %v", g["created_at"])
	}
	if cb, ok := g["created_by"].(map[string]any); !ok || cb["id"] != "a1" {
		t.Errorf("created_by: %v", g["created_by"])
	}
}

// TestFormatError pins the prefix for every domain sentinel. Any new sentinel
// added to internal/domain/errors.go will need a clause here.
func TestFormatError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		prefix string
	}{
		{"invalid", fmt.Errorf("%w: bad slug", domain.ErrInvalidArgument), "invalid argument: bad slug"},
		{"notfound", fmt.Errorf("%w: ticket xyz", domain.ErrNotFound), "not found: ticket xyz"},
		{"exists", fmt.Errorf("%w: project demo", domain.ErrAlreadyExists), "already exists: project demo"},
		{"precond", fmt.Errorf("%w: still active", domain.ErrFailedPrecondition), "precondition failed: still active"},
		{"unauth", fmt.Errorf("%w: session expired", domain.ErrUnauthenticated), "unauthenticated: session expired"},
	}
	for _, tc := range cases {
		got := formatError(tc.err)
		if got != tc.prefix {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.prefix)
		}
	}

	// Wrapped sentinel through another wrapping layer still maps.
	deep := fmt.Errorf("walk: %w", fmt.Errorf("%w: missing", domain.ErrNotFound))
	if got := formatError(deep); !strings.HasPrefix(got, "not found:") {
		t.Errorf("wrapped: got %q", got)
	}

	// Non-sentinel error returns the raw message verbatim so it isn't
	// silently swallowed.
	other := errors.New("disk full")
	if got := formatError(other); got != "disk full" {
		t.Errorf("non-sentinel: got %q", got)
	}

	// Nil error returns empty string.
	if got := formatError(nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
}

// TestStringSliceFromAny ensures the JSON-decoded []any path coerces into
// []string and drops non-strings; the typed []string fast-path round-trips.
func TestStringSliceFromAny(t *testing.T) {
	if got := stringSliceFromAny(nil); got != nil {
		t.Errorf("nil: %v", got)
	}
	if got := stringSliceFromAny([]string{"a", "b"}); len(got) != 2 || got[0] != "a" {
		t.Errorf("typed: %v", got)
	}
	if got := stringSliceFromAny([]any{"a", 42, "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("[]any: %v", got)
	}
	if got := stringSliceFromAny("not-a-slice"); got != nil {
		t.Errorf("scalar: %v", got)
	}
}

// extractText unwraps the single text-content block produced by
// mcp.NewToolResultText into a plain string for further parsing.
func extractText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil {
		t.Fatalf("nil CallToolResult")
	}
	if len(res.Content) == 0 {
		t.Fatalf("no content in CallToolResult: %+v", res)
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("first content not TextContent: %T", res.Content[0])
	}
	return tc.Text
}

// sortedNames returns a sorted slice of registered tool names. Used in error
// messages so test failures show a stable list.
func sortedNames(m map[string]*mcpserver.ServerTool) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
