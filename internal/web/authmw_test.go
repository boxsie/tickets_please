package web

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/auth"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/svc"
)

// userCookieFor mints a valid signed tp_user cookie for the given user id.
func userCookieFor(a *app, userID string) *http.Cookie {
	val := userID + "." + base64.RawURLEncoding.EncodeToString(a.session.signWith(userCookiePurpose, userID))
	return &http.Cookie{Name: userCookieName, Value: val}
}

// guardFixture builds an auth-enabled app with one project ("guarded") and
// users at each role, then mounts viewer/member/owner-gated sentinel routes so
// the role matrix can be asserted without depending on real handler behavior.
type guardFixture struct {
	a   *app
	srv *httptest.Server
}

func newGuardFixture(t *testing.T) *guardFixture {
	t.Helper()
	deps := freshDeps(t)
	s := deps.Service
	a := newApp(deps)
	// Flip on auth (newApp computed false from the empty config).
	a.providers = map[string]auth.Provider{"github": &fakeProvider{name: "github", claims: &auth.Claims{}}}
	a.authEnabled = true

	// A project to guard. CreateProject needs an authenticated agent session.
	agentID, _, err := s.RegisterAgent(context.Background(), "guard-fixture", "Guard Fixture", nil, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	ctx := svc.WithSessionID(context.Background(), agentID)
	proj, err := s.CreateProject(ctx, "guarded", "Guarded", "test", strings.Repeat("z", 220))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Users + memberships at each role.
	now := time.Now().UTC()
	for id := range map[string]struct{}{"u-viewer": {}, "u-member": {}, "u-owner": {}, "u-stranger": {}} {
		if err := s.UserStore.WriteUser(&store.UserRecord{
			ID: id, Email: id + "@example.com", DisplayName: id, CreatedAt: now, LastLoginAt: now,
		}); err != nil {
			t.Fatalf("write user %s: %v", id, err)
		}
	}
	grants := map[string]domain.Role{"u-viewer": domain.RoleViewer, "u-member": domain.RoleMember, "u-owner": domain.RoleOwner}
	for uid, role := range grants {
		if _, err := s.MembershipStore.GrantMembership(context.Background(), &store.MembershipRecord{
			UserID: uid, ProjectID: proj.ID, Role: role,
		}); err != nil {
			t.Fatalf("grant %s: %v", uid, err)
		}
	}

	// Sentinel-backed guarded routes (chain minus CSRF, which is tested
	// separately — this isolates the role guard). 299 = "guard passed".
	sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) })
	guard := func(min domain.Role) http.Handler {
		return a.session.middleware(a.authMiddleware(a.requireSlugRole(min, sentinel)))
	}
	mux := http.NewServeMux()
	mux.Handle("GET /p/{slug}", guard(domain.RoleViewer))
	mux.Handle("POST /p/{slug}/summary", guard(domain.RoleMember))
	mux.Handle("POST /p/{slug}/delete", guard(domain.RoleOwner))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &guardFixture{a: a, srv: srv}
}

// req issues a request as the given user (empty userID = unauthenticated),
// without a cookie jar so cases don't leak into each other.
func (f *guardFixture) req(t *testing.T, method, path, userID string) *http.Response {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	rq, err := http.NewRequest(method, f.srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if userID != "" {
		rq.AddCookie(userCookieFor(f.a, userID))
	}
	resp, err := client.Do(rq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestGuard_UnauthenticatedRedirectsToLogin(t *testing.T) {
	f := newGuardFixture(t)
	resp := f.req(t, http.MethodGet, "/p/guarded", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/auth/login") {
		t.Fatalf("Location = %q, want /auth/login...", loc)
	}
}

func TestGuard_AuthenticatedNoMembershipIs403(t *testing.T) {
	f := newGuardFixture(t)
	resp := f.req(t, http.MethodGet, "/p/guarded", "u-stranger")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestGuard_ViewerCanGetButNotMemberPost(t *testing.T) {
	f := newGuardFixture(t)

	get := f.req(t, http.MethodGet, "/p/guarded", "u-viewer")
	get.Body.Close()
	if get.StatusCode != 299 {
		t.Fatalf("viewer GET = %d, want 299 (guard passed)", get.StatusCode)
	}

	post := f.req(t, http.MethodPost, "/p/guarded/summary", "u-viewer")
	post.Body.Close()
	if post.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer POST member-route = %d, want 403", post.StatusCode)
	}
}

func TestGuard_MemberCanPostButNotOwnerDelete(t *testing.T) {
	f := newGuardFixture(t)

	post := f.req(t, http.MethodPost, "/p/guarded/summary", "u-member")
	post.Body.Close()
	if post.StatusCode != 299 {
		t.Fatalf("member POST = %d, want 299 (guard passed)", post.StatusCode)
	}

	del := f.req(t, http.MethodPost, "/p/guarded/delete", "u-member")
	del.Body.Close()
	if del.StatusCode != http.StatusForbidden {
		t.Fatalf("member DELETE owner-route = %d, want 403", del.StatusCode)
	}
}

func TestGuard_OwnerCanDelete(t *testing.T) {
	f := newGuardFixture(t)
	del := f.req(t, http.MethodPost, "/p/guarded/delete", "u-owner")
	del.Body.Close()
	if del.StatusCode != 299 {
		t.Fatalf("owner DELETE = %d, want 299 (guard passed)", del.StatusCode)
	}
}

// TestGuard_DisabledIsPassthrough confirms the localhost no-auth mode: with no
// providers configured, the guards never fire even without a cookie.
func TestGuard_DisabledIsPassthrough(t *testing.T) {
	deps := freshDeps(t)
	a := newApp(deps) // no providers → authEnabled false
	if a.authEnabled {
		t.Fatal("expected authEnabled false with no providers")
	}
	sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) })
	mux := http.NewServeMux()
	mux.Handle("GET /p/{slug}", a.session.middleware(a.authMiddleware(a.requireSlugRole(domain.RoleOwner, sentinel))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/p/anything")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 299 {
		t.Fatalf("auth-disabled guard = %d, want 299 (passthrough)", resp.StatusCode)
	}
}
