package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/auth"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/svc"
)

// newBootstrapApp builds an auth-enabled app plus a couple of projects so the
// owner-of-all backfill has something to grant against. Returns the app and
// the two project ids.
func newBootstrapApp(t *testing.T) (*app, string, string) {
	t.Helper()
	deps := freshDeps(t)
	a := newApp(deps)
	a.providers = map[string]auth.Provider{"github": &fakeProvider{name: "github", claims: &auth.Claims{}}}
	a.authEnabled = true

	agentID, _, err := deps.Service.RegisterAgent(context.Background(), "bootstrap-fixture", "Bootstrap Fixture", nil, 5*time.Minute)
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	ctx := svc.WithSessionID(context.Background(), agentID)
	// The store is one-project-per-data-dir, so the first project is created in
	// the Service's own DataDir and the second is a separately-mounted repo —
	// together they exercise the "owner of *every* project" backfill across
	// multiple mounted stores.
	if _, err := deps.Service.CreateProject(ctx, "proj-a", "Proj A", "desc", strings.Repeat("z", 220)); err != nil {
		t.Fatalf("CreateProject a: %v", err)
	}
	repoB := seedRepoOnDisk(t, t.TempDir(), "repo-b", "proj-b")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repoB); err != nil {
		t.Fatalf("RegisterProjectMount b: %v", err)
	}
	// Resolve the canonical project IDs from the same source the backfill
	// walks (ListProjects), so the test keys memberships exactly as the code
	// under test does — independent of mount.ID vs ProjectID semantics.
	return a, projectIDBySlug(t, a, "proj-a"), projectIDBySlug(t, a, "proj-b")
}

func projectIDBySlug(t *testing.T, a *app, slug string) string {
	t.Helper()
	projects, err := a.deps.Service.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range projects {
		if p.Slug == slug {
			return p.ID
		}
	}
	t.Fatalf("project %q not found in ListProjects", slug)
	return ""
}

// writeUser inserts a user record and returns its id.
func (a *app) testWriteUser(t *testing.T, id, provider, subject string) string {
	t.Helper()
	rec := &store.UserRecord{
		ID:          id,
		DisplayName: id,
		CreatedAt:   time.Now().UTC(),
		LastLoginAt: time.Now().UTC(),
	}
	sub := subject
	switch provider {
	case "github":
		rec.GitHubLogin = &sub
	case "google":
		rec.GoogleSub = &sub
	}
	if err := a.deps.Service.UserStore.WriteUser(rec); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}
	return id
}

func ownsBoth(t *testing.T, a *app, userID, p1, p2 string) bool {
	t.Helper()
	ms := a.deps.Service.MembershipStore
	for _, pid := range []string{p1, p2} {
		m, err := ms.GetMembership(pid, userID)
		if err != nil || m.Role != domain.RoleOwner {
			return false
		}
	}
	return true
}

// Empty users store + login → first-login-wins promotes the user to owner of
// every existing project.
func TestBootstrap_FirstLoginWins(t *testing.T) {
	t.Setenv(bootstrapAdminEnv, "")
	a, p1, p2 := newBootstrapApp(t)

	claims := &auth.Claims{Provider: "github", Subject: "boxsie", Email: "dan@example.com"}
	uid := a.testWriteUser(t, "u-first", "github", "boxsie")

	a.maybeBootstrapAdmin(context.Background(), claims, uid)

	if !ownsBoth(t, a, uid, p1, p2) {
		t.Fatal("first user should own every project after first-login-wins")
	}
}

// Non-empty users store + no env → a newly-arriving user gets zero memberships.
func TestBootstrap_SecondUserNoGrant(t *testing.T) {
	t.Setenv(bootstrapAdminEnv, "")
	a, p1, p2 := newBootstrapApp(t)

	// First user already exists (the implicit owner).
	a.testWriteUser(t, "u-first", "github", "alice")

	// Second user logs in.
	claims := &auth.Claims{Provider: "github", Subject: "bob", Email: "bob@example.com"}
	uid := a.testWriteUser(t, "u-second", "github", "bob")

	a.maybeBootstrapAdmin(context.Background(), claims, uid)

	ms := a.deps.Service.MembershipStore
	for _, pid := range []string{p1, p2} {
		if _, err := ms.GetMembership(pid, uid); err == nil {
			t.Fatalf("second user must not be granted on project %s", pid)
		}
	}
}

// Env-set login → the matching identity becomes owner of all, even when the
// store is non-empty (so it doubles as a recovery hook).
func TestBootstrap_EnvOverrideMatch(t *testing.T) {
	a, p1, p2 := newBootstrapApp(t)
	// Pre-existing user so first-login-wins can't be what fires.
	a.testWriteUser(t, "u-noise", "github", "noise")

	t.Setenv(bootstrapAdminEnv, "github:boxsie")
	claims := &auth.Claims{Provider: "github", Subject: "boxsie", Email: "dan@example.com"}
	uid := a.testWriteUser(t, "u-admin", "github", "boxsie")

	a.maybeBootstrapAdmin(context.Background(), claims, uid)

	if !ownsBoth(t, a, uid, p1, p2) {
		t.Fatal("env-named admin should own every project")
	}
}

// Env override matching on email (Google's stable subject is the opaque `sub`,
// so operators configure the email instead).
func TestBootstrap_EnvOverrideMatchesEmail(t *testing.T) {
	a, p1, p2 := newBootstrapApp(t)
	a.testWriteUser(t, "u-noise", "google", "111")

	t.Setenv(bootstrapAdminEnv, "google:dan@example.com")
	claims := &auth.Claims{Provider: "google", Subject: "99887766", Email: "dan@example.com"}
	uid := a.testWriteUser(t, "u-admin", "google", "99887766")

	a.maybeBootstrapAdmin(context.Background(), claims, uid)

	if !ownsBoth(t, a, uid, p1, p2) {
		t.Fatal("env admin matched by email should own every project")
	}
}

// Env set but naming a different identity → no grant, and (since the store is
// non-empty) first-login-wins doesn't fire either.
func TestBootstrap_EnvOverrideNoMatch(t *testing.T) {
	a, p1, _ := newBootstrapApp(t)
	a.testWriteUser(t, "u-noise", "github", "someone")

	t.Setenv(bootstrapAdminEnv, "github:boxsie")
	claims := &auth.Claims{Provider: "github", Subject: "intruder", Email: "x@example.com"}
	uid := a.testWriteUser(t, "u-intruder", "github", "intruder")

	a.maybeBootstrapAdmin(context.Background(), claims, uid)

	if _, err := a.deps.Service.MembershipStore.GetMembership(p1, uid); err == nil {
		t.Fatal("non-matching identity must not be granted owner")
	}
}

func TestBootstrapSpecMatches(t *testing.T) {
	c := &auth.Claims{Provider: "github", Subject: "boxsie", Email: "dan@example.com"}
	cases := []struct {
		spec string
		want bool
	}{
		{"github:boxsie", true},
		{"github:dan@example.com", true},
		{"github: boxsie ", true}, // trimmed
		{"google:boxsie", false},  // wrong provider
		{"github:someoneelse", false},
		{"github", false}, // no colon
		{"", false},
	}
	for _, tc := range cases {
		if got := bootstrapSpecMatches(tc.spec, c); got != tc.want {
			t.Errorf("bootstrapSpecMatches(%q) = %v, want %v", tc.spec, got, tc.want)
		}
	}
}
