package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/svc"
)

// seedUser writes a user record straight into the central user store (users are
// normally minted by the OAuth login flow, which tests don't exercise).
func seedUser(t *testing.T, deps Deps, id, name, email string) {
	t.Helper()
	if err := deps.Service.UserStore.WriteUser(&store.UserRecord{
		ID:          id,
		Email:       email,
		DisplayName: name,
		CreatedAt:   time.Now(),
		LastLoginAt: time.Now(),
	}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}
}

// TestUser_Detail_Renders: /u/{id} shows the user's identity + membership block.
func TestUser_Detail_Renders(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	seedUser(t, deps, "user-dan", "Dan", "dan@example.com")

	body := getBody(t, client, srv.URL+"/u/user-dan")
	for _, want := range []string{"Dan", "dan@example.com", "Member of"} {
		if !strings.Contains(body, want) {
			t.Errorf("user page missing %q", want)
		}
	}
}

// TestUser_Detail_ShowsMemberships: a granted membership renders with the
// project name + role.
func TestUser_Detail_ShowsMemberships(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _ := seedProjectAndTicket(t, deps, "umemproj", "x")
	proj, err := deps.Service.GetProject(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	seedUser(t, deps, "user-mem", "Member Mary", "mary@example.com")
	if _, err := deps.Service.MembershipStore.GrantMembership(context.Background(), &store.MembershipRecord{
		UserID:    "user-mem",
		ProjectID: proj.ID,
		Role:      domain.RoleMember,
		GrantedAt: time.Now(),
	}); err != nil {
		t.Fatalf("GrantMembership: %v", err)
	}

	body := getBody(t, client, srv.URL+"/u/user-mem")
	if !strings.Contains(body, "umemproj") {
		t.Errorf("membership project name missing from user page")
	}
	if !strings.Contains(body, string(domain.RoleMember)) {
		t.Errorf("membership role missing from user page")
	}
}

// TestUser_Detail_NotFound: an unknown user id 404s.
func TestUser_Detail_NotFound(t *testing.T) {
	srv, client, _ := freshServerWithDeps(t)
	resp, err := client.Get(srv.URL + "/u/nobody")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestComment_AuthorLinks: a comment authored by an agent acting for a user
// renders both names as links (/agents/{id} and /u/{id}) inside comment-author.
func TestComment_AuthorLinks(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, tid := seedProjectAndTicket(t, deps, "clink", "Comment Links")

	// Register an agent acting for a seeded user (granted membership so it may
	// mutate), then comment as it.
	seedUser(t, deps, "user-dan2", "Dan", "dan@x.com")
	proj, err := deps.Service.GetProject(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if _, err := deps.Service.MembershipStore.GrantMembership(context.Background(), &store.MembershipRecord{
		UserID: "user-dan2", ProjectID: proj.ID, Role: domain.RoleMember, GrantedAt: time.Now(),
	}); err != nil {
		t.Fatalf("GrantMembership: %v", err)
	}
	aid, _, err := deps.Service.RegisterAgent(context.Background(), "acting-key", "Claude",
		map[string]string{"client_name": "test"}, 5*time.Minute, "user-dan2")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if _, err := deps.Service.CreateComment(svc.WithSessionID(context.Background(), aid), tid, "a note"); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	body := getBody(t, client, srv.URL+"/tickets/"+tid+"?slug="+slug)
	if !strings.Contains(body, `href="/agents/`+aid+`"`) {
		t.Errorf("comment author should link the agent to /agents/%s\n", aid)
	}
	if !strings.Contains(body, `href="/u/user-dan2"`) {
		t.Errorf("comment acting-for should link the user to /u/user-dan2")
	}
	if !strings.Contains(body, "(for ") {
		t.Errorf("comment author should show the acting-for suffix")
	}
}
