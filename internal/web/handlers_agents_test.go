package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	agentspg "tickets_please/internal/web/components/pages/agents"
)

// freshServerWithAgents builds a test server and pre-registers the named agents
// through the service, returning the server + client. Each agent gets a model
// in its metadata so the Model column has something to render.
func freshServerWithAgents(t *testing.T, names ...string) (*httptest.Server, *http.Client, Deps) {
	t.Helper()
	deps := freshDeps(t)
	ctx := context.Background()
	for i, name := range names {
		key := "test:" + name
		if _, _, err := deps.Service.RegisterAgent(ctx, key, name, map[string]string{
			"model":       "opus-4." + string(rune('0'+i)),
			"client_name": "Claude Code",
		}, 0, ""); err != nil {
			t.Fatalf("RegisterAgent %s: %v", name, err)
		}
	}

	mux := http.NewServeMux()
	Mount(mux, deps)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return srv, client, deps
}

func TestAgents_Index_RendersAllAgents(t *testing.T) {
	srv, client, _ := freshServerWithAgents(t, "Alice", "Bob")

	resp, err := client.Get(srv.URL + "/agents")
	if err != nil {
		t.Fatalf("GET /agents: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"Alice", "Bob"} {
		if !strings.Contains(body, want) {
			t.Errorf("/agents missing agent name %q\n%s", want, body)
		}
	}
	// Heading + table + filter box + sortable headers all present.
	if !strings.Contains(body, "<h1>Agents</h1>") {
		t.Errorf("/agents missing page heading:\n%s", body)
	}
	if !strings.Contains(body, "data-agents-table") || !strings.Contains(body, "data-agents-filter") {
		t.Errorf("/agents missing table/filter scaffolding:\n%s", body)
	}
	// Row click target / detail link to the per-agent page.
	if !strings.Contains(body, `data-href="/agents/`) || !strings.Contains(body, `href="/agents/`) {
		t.Errorf("/agents rows missing /agents/{id} links:\n%s", body)
	}
	// The model from metadata renders.
	if !strings.Contains(body, "opus-4.0") {
		t.Errorf("/agents missing model label:\n%s", body)
	}
}

// TestAgents_Index_EmptyState renders the page component directly with no
// rows. (The HTTP path can't reach a truly empty registry — the web session
// auto-registers a "Web UI" agent on the first request — so the empty state is
// exercised at the component level.)
func TestAgents_Index_EmptyState(t *testing.T) {
	var sb strings.Builder
	if err := agentspg.Index(agentspg.IndexProps{}).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "No agents have registered yet") {
		t.Errorf("empty state missing copy:\n%s", out)
	}
	if strings.Contains(out, "data-agents-table") {
		t.Errorf("empty state should not render the table:\n%s", out)
	}
}

func TestAgents_SidebarLink(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, `href="/agents"`) {
		t.Errorf("sidebar missing Agents link:\n%s", body)
	}
}
