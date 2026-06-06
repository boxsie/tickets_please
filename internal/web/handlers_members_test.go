package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestMembers_PageRenders: the members page renders the invite form and the
// empty members state for a fresh project (auth disabled in tests, so the
// owner-only guard is a pass-through).
func TestMembers_PageRenders(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	seedProjectAndTicket(t, deps, "mem", "Members Proj")
	resp, err := client.Get(srv.URL + "/p/mem/members")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Invite someone") {
		t.Errorf("members page missing invite form\n%s", body)
	}
}

// TestMembers_InviteCreatesPendingLink: POST invite → 303, and the pending
// invitation (with its inline /invite/{token} link) shows on the page.
func TestMembers_InviteCreatesPendingLink(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	seedProjectAndTicket(t, deps, "memi", "Members Invite")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"email": {"new@example.com"}, "role": {"member"}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/p/memi/members/invite", form)
	if err != nil {
		t.Fatalf("POST invite: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("invite status = %d, want 303", resp.StatusCode)
	}
	// The invite is persisted with a token.
	invites, err := deps.Service.ListInvitations(context.Background(), "memi")
	if err != nil || len(invites) != 1 {
		t.Fatalf("ListInvitations = %d (%v), want 1", len(invites), err)
	}
	// And it surfaces on the page with its inline accept link + email.
	resp, err = client.Get(srv.URL + "/p/memi/members")
	if err != nil {
		t.Fatalf("GET members: %v", err)
	}
	body := mustReadAll(t, resp)
	if !strings.Contains(body, "new@example.com") {
		t.Errorf("pending invite email missing\n%s", body)
	}
	if !strings.Contains(body, "/invite/"+invites[0].Token) {
		t.Errorf("inline accept link missing\n%s", body)
	}
}

// TestMembers_AcceptRequiresLogin: in auth-disabled mode there's no user to
// grant to, so the accept route refuses rather than silently no-op.
func TestMembers_AcceptRequiresLogin(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	seedProjectAndTicket(t, deps, "mema", "Members Accept")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"email": {"x@example.com"}, "role": {"viewer"}, "_csrf": {csrf}}
	if _, err := client.PostForm(srv.URL+"/p/mema/members/invite", form); err != nil {
		t.Fatalf("POST invite: %v", err)
	}
	invites, err := deps.Service.ListInvitations(context.Background(), "mema")
	if err != nil || len(invites) != 1 {
		t.Fatalf("ListInvitations = %d (%v), want 1", len(invites), err)
	}
	resp, err := client.Get(srv.URL + "/invite/" + invites[0].Token)
	if err != nil {
		t.Fatalf("GET accept: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("accept without login = %d, want 403", resp.StatusCode)
	}
}
