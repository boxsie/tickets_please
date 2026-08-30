package mcptools

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"tickets_please/internal/config"
)

// searchReq builds a bare search request with the supplied arguments.
func searchReq(args map[string]any) mcp.CallToolRequest {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	return req
}

// TestResolveSearchScope_DefaultsToBoundProject is the whole point of the
// scope helper: a search with no scope arguments searches the session's
// project, not every mounted project.
func TestResolveSearchScope_DefaultsToBoundProject(t *testing.T) {
	reg := NewRegistry(config.Config{})
	_ = reg.Register("stdio", &Session{ProjectSlug: "session-slug"})
	tools := NewTools(nil, reg, nil)

	got, err := tools.resolveSearchScope(context.Background(), searchReq(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "session-slug" {
		t.Errorf("scope = %q; want the bound project %q", got, "session-slug")
	}
}

// TestResolveSearchScope_ExplicitSlugWins: naming a project still beats the
// session default, same as every other project-scoped tool.
func TestResolveSearchScope_ExplicitSlugWins(t *testing.T) {
	reg := NewRegistry(config.Config{})
	_ = reg.Register("stdio", &Session{ProjectSlug: "session-slug"})
	tools := NewTools(nil, reg, nil)

	got, err := tools.resolveSearchScope(context.Background(),
		searchReq(map[string]any{"project_id_or_slug": "explicit"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "explicit" {
		t.Errorf("scope = %q; want %q", got, "explicit")
	}
}

// TestResolveSearchScope_CrossProjectClearsScope: the opt-in yields the empty
// scope svc reads as "every mount" — and it works with no project bound,
// since a global sweep needs no session project.
func TestResolveSearchScope_CrossProjectClearsScope(t *testing.T) {
	for name, sess := range map[string]*Session{
		"bound session":   {ProjectSlug: "session-slug"},
		"unbound session": {},
	} {
		t.Run(name, func(t *testing.T) {
			reg := NewRegistry(config.Config{})
			_ = reg.Register("stdio", sess)
			tools := NewTools(nil, reg, nil)

			got, err := tools.resolveSearchScope(context.Background(),
				searchReq(map[string]any{"cross_project": true}))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != "" {
				t.Errorf("scope = %q; want empty (all mounts)", got)
			}
		})
	}
}

// TestResolveSearchScope_CrossProjectWithExplicitSlug_Errors: asking for one
// project AND every project is contradictory; say so rather than silently
// honouring one of them.
func TestResolveSearchScope_CrossProjectWithExplicitSlug_Errors(t *testing.T) {
	reg := NewRegistry(config.Config{})
	_ = reg.Register("stdio", &Session{ProjectSlug: "session-slug"})
	tools := NewTools(nil, reg, nil)

	_, err := tools.resolveSearchScope(context.Background(), searchReq(map[string]any{
		"cross_project":      true,
		"project_id_or_slug": "explicit",
	}))
	if err == nil {
		t.Fatal("expected an error for contradictory scope arguments")
	}
	if !strings.Contains(err.Error(), "cross_project") {
		t.Errorf("error should name cross_project, got %q", err.Error())
	}
}

// TestResolveSearchScope_CrossProjectFalseIsNotAnOptIn guards the obvious
// footgun: an explicit `cross_project: false` must still take the default
// path rather than reading as "the key is present, so sweep everything".
func TestResolveSearchScope_CrossProjectFalseIsNotAnOptIn(t *testing.T) {
	reg := NewRegistry(config.Config{})
	_ = reg.Register("stdio", &Session{ProjectSlug: "session-slug"})
	tools := NewTools(nil, reg, nil)

	got, err := tools.resolveSearchScope(context.Background(),
		searchReq(map[string]any{"cross_project": false}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "session-slug" {
		t.Errorf("scope = %q; want the bound project %q", got, "session-slug")
	}
}

// TestSearchLearnings_UnboundSessionErrors pins the behaviour change at the
// handler: search_learnings used to fall back to searching every mounted
// project when nothing was bound. Now it asks to be pointed at a project —
// unless the caller opts into the global sweep.
func TestSearchLearnings_UnboundSessionErrors(t *testing.T) {
	tools, _, _ := freshToolsForRegister(t)
	if err := tools.registry.Register("stdio", &Session{
		AgentID:   "agent-x",
		AgentKey:  "test:x",
		AgentName: "Tester",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res, err := tools.handleSearchLearnings(context.Background(),
		searchReq(map[string]any{"query": "anything"}))
	if err != nil {
		t.Fatalf("handleSearchLearnings: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected an error result, got success: %s", extractText(t, res))
	}
	if msg := extractText(t, res); !strings.Contains(msg, "register_agent") {
		t.Errorf("expected the register_agent hint, got %q", msg)
	}

	// The opt-in is the escape hatch: no bound project, but an explicit
	// global sweep is a legitimate request.
	res, err = tools.handleSearchLearnings(context.Background(),
		searchReq(map[string]any{"query": "anything", "cross_project": true}))
	if err != nil {
		t.Fatalf("handleSearchLearnings(cross_project): %v", err)
	}
	if res.IsError {
		t.Fatalf("cross_project sweep should not error: %s", extractText(t, res))
	}
}

// TestSearchComments_UnboundSessionErrors mirrors the learnings case — the two
// tools fanned out by default together, so they get scoped together.
func TestSearchComments_UnboundSessionErrors(t *testing.T) {
	tools, _, _ := freshToolsForRegister(t)
	if err := tools.registry.Register("stdio", &Session{
		AgentID:   "agent-y",
		AgentKey:  "test:y",
		AgentName: "Tester",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res, err := tools.handleSearchComments(context.Background(),
		searchReq(map[string]any{"query": "anything"}))
	if err != nil {
		t.Fatalf("handleSearchComments: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected an error result, got success: %s", extractText(t, res))
	}
	if msg := extractText(t, res); !strings.Contains(msg, "register_agent") {
		t.Errorf("expected the register_agent hint, got %q", msg)
	}
}
