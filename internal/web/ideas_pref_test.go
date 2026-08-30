package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
)

// ideasFixture seeds a project with one work ticket and one idea ticket and
// returns the slug + the two unique titles.
func ideasFixture(t *testing.T, deps Deps) (slug, work, idea, ideaID string) {
	t.Helper()
	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "idea-fixture", "idea-fixture",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, id)
	slug = "idx"
	if _, err := deps.Service.CreateProject(authed, slug, slug, "test", strings.Repeat("z", 220)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	work, idea = "WORKVISIBLE", "SPITBALLIDEA"
	if _, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: work}); err != nil {
		t.Fatalf("CreateTicket work: %v", err)
	}
	ik, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: idea, Kind: domain.KindIdea})
	if err != nil {
		t.Fatalf("CreateTicket idea: %v", err)
	}
	return slug, work, idea, ik.ID
}

// TestIdeas_Overview_TogglesVisibility: the dashboard keeps ideas out of the
// work board by default (lane collapsed), and lists them when
// ?include_ideas=true — which persists the cookie and flips the toggle on.
func TestIdeas_Overview_TogglesVisibility(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, work, idea, _ := ideasFixture(t, deps)

	// Default: work shown, idea title hidden, toggle present + off.
	resp, err := client.Get(srv.URL + "/p/" + slug)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if !strings.Contains(body, work) {
		t.Errorf("overview should show the work ticket")
	}
	if strings.Contains(body, idea) {
		t.Errorf("overview should not list idea titles by default (lane collapsed)")
	}
	if !strings.Contains(body, "Show ideas") {
		t.Errorf("overview missing the ideas toggle")
	}

	// With the param: the idea lane lists the idea, cookie set.
	resp, err = client.Get(srv.URL + "/p/" + slug + "?include_ideas=true")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body = mustReadAll(t, resp)
	if !strings.Contains(body, idea) {
		t.Errorf("overview should list the idea when ?include_ideas=true")
	}
	if !setsCookie(resp, showIdeasCookie, "1") {
		t.Errorf("expected tp_show_ideas=1 cookie to be set")
	}
}

// TestPromote_Web_FlipsIdea: POSTing the promote form turns an idea into a work
// ticket and 303s back to the detail page.
func TestPromote_Web_FlipsIdea(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _, _, ideaID := ideasFixture(t, deps)
	csrf := primeCSRF(t, client, srv.URL)

	form := url.Values{"comment": {"ready to build"}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/tickets/"+ideaID+"/promote?slug="+slug, form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	got, err := deps.Service.GetTicket(context.Background(), ideaID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.Kind != domain.KindWork {
		t.Errorf("idea should be promoted to work, got kind %q", got.Kind)
	}

	// Now visible on the default board.
	body := getBody(t, client, srv.URL+"/p/"+slug)
	if !strings.Contains(body, "SPITBALLIDEA") {
		t.Errorf("promoted ticket should appear on the default board")
	}
}
