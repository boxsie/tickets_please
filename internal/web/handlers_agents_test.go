package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
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

// seedAgentWork registers an agent, creates a project, and produces n tickets
// (each moved to in_progress with a comment) authored by that agent. Returns the
// deps + the agent id so a detail test can drive the HTTP path.
func seedAgentWork(t *testing.T, n int) (*httptest.Server, *http.Client, string) {
	t.Helper()
	deps := freshDeps(t)
	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "test:worker", "Worker", map[string]string{"model": "opus", "client_name": "Claude Code"}, 0, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	actx := svc.WithSessionID(ctx, id)
	if _, err := deps.Service.CreateProject(actx, "alpha", "Alpha", "", strings.Repeat("Summary content here. ", 12)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for i := range n {
		tk, err := deps.Service.CreateTicket(actx, domain.CreateTicketInput{ProjectIDOrSlug: "alpha", Title: "ticket-" + strconv.Itoa(i)})
		if err != nil {
			t.Fatalf("CreateTicket: %v", err)
		}
		if _, err := deps.Service.MoveTicket(actx, tk.ID, domain.ColumnInProgress, "go"); err != nil {
			t.Fatalf("MoveTicket: %v", err)
		}
	}

	mux := http.NewServeMux()
	Mount(mux, deps)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	return srv, client, id
}

func TestAgents_Detail_RendersHeaderAndActivity(t *testing.T) {
	srv, client, id := seedAgentWork(t, 3)
	resp, err := client.Get(srv.URL + "/agents/" + id)
	if err != nil {
		t.Fatalf("GET /agents/{id}: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "<h1>Worker</h1>") {
		t.Errorf("detail missing agent name header:\n%s", body)
	}
	// Identity + counters.
	for _, want := range []string{"Registered", "Last seen", "Tickets created", "agent-activity"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}
	// Currently-working callout (the in_progress tickets it created).
	if !strings.Contains(body, "Currently working on") {
		t.Errorf("detail missing current-work callout:\n%s", body)
	}
	// Activity feed renders the ticket-created entries through the ticket card.
	if !strings.Contains(body, "activity-feed") || !strings.Contains(body, "created a ticket") {
		t.Errorf("detail missing activity rows:\n%s", body)
	}
	if !strings.Contains(body, "registered with the server") {
		t.Errorf("detail missing the registration anchor row:\n%s", body)
	}
}

func TestAgents_Detail_Paginates(t *testing.T) {
	// 60 created + 60 move-comments + 1 registration ≈ 121 activity items, so
	// page 0 fills (50) and there's a second page.
	srv, client, id := seedAgentWork(t, 60)

	resp, err := client.Get(srv.URL + "/agents/" + id)
	if err != nil {
		t.Fatal(err)
	}
	body := mustReadAll(t, resp)
	if !strings.Contains(body, "Older →") {
		t.Errorf("page 0 should offer an older page:\n%s", firstN(body, 4000))
	}
	if strings.Contains(body, "← Newer") {
		t.Errorf("page 0 should NOT offer a newer page")
	}

	resp2, err := client.Get(srv.URL + "/agents/" + id + "?page=1")
	if err != nil {
		t.Fatal(err)
	}
	body2 := mustReadAll(t, resp2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("page 1 status = %d", resp2.StatusCode)
	}
	if !strings.Contains(body2, "← Newer") {
		t.Errorf("page 1 should offer a newer page:\n%s", firstN(body2, 4000))
	}
}

func TestAgents_Detail_UnknownID(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/agents/no-such-agent")
	if err != nil {
		t.Fatal(err)
	}
	mustReadAll(t, resp)
	if resp.StatusCode == http.StatusOK {
		t.Errorf("expected non-200 for unknown agent, got %d", resp.StatusCode)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
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
