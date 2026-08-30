package store

import (
	"context"
	"testing"
	"time"

	"tickets_please/internal/domain"
)

func TestMembershipStore_NewCreatesDirs(t *testing.T) {
	ms, err := NewMembershipStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewMembershipStore: %v", err)
	}
	if got := ms.membershipsDir(); got == "" {
		t.Fatal("membershipsDir empty")
	}
}

func TestMembershipStore_GrantThenList(t *testing.T) {
	ms, err := NewMembershipStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewMembershipStore: %v", err)
	}
	ctx := context.Background()
	out, err := ms.GrantMembership(ctx, &MembershipRecord{
		UserID:    "dan",
		ProjectID: "proj-1",
		Role:      domain.RoleOwner,
		GrantedBy: "system",
	})
	if err != nil {
		t.Fatalf("GrantMembership: %v", err)
	}
	if out.Role != domain.RoleOwner {
		t.Errorf("role: got %q want owner", out.Role)
	}
	if out.GrantedAt.IsZero() {
		t.Error("GrantedAt was not stamped")
	}

	members, err := ms.ListMembersOfProject("proj-1")
	if err != nil {
		t.Fatalf("ListMembersOfProject: %v", err)
	}
	if len(members) != 1 || members[0].UserID != "dan" {
		t.Errorf("members: %+v", members)
	}

	mine, err := ms.ListMembershipsForUser("dan")
	if err != nil {
		t.Fatalf("ListMembershipsForUser: %v", err)
	}
	if len(mine) != 1 || mine[0].ProjectID != "proj-1" {
		t.Errorf("mine: %+v", mine)
	}
}

func TestMembershipStore_GrantIsIdempotent(t *testing.T) {
	ms, err := NewMembershipStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewMembershipStore: %v", err)
	}
	ctx := context.Background()
	first, err := ms.GrantMembership(ctx, &MembershipRecord{
		UserID:    "dan",
		ProjectID: "proj-1",
		Role:      domain.RoleMember,
		GrantedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Same role → idempotent (returns existing, doesn't bump GrantedAt).
	again, err := ms.GrantMembership(ctx, &MembershipRecord{
		UserID:    "dan",
		ProjectID: "proj-1",
		Role:      domain.RoleMember,
		GrantedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !again.GrantedAt.Equal(first.GrantedAt) {
		t.Errorf("idempotent grant changed GrantedAt: %v → %v", first.GrantedAt, again.GrantedAt)
	}

	// Different role → overwrite.
	upgraded, err := ms.GrantMembership(ctx, &MembershipRecord{
		UserID:    "dan",
		ProjectID: "proj-1",
		Role:      domain.RoleOwner,
		GrantedBy: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Role != domain.RoleOwner {
		t.Errorf("role upgrade not applied: %+v", upgraded)
	}
	// And the stored record reflects it.
	reread, err := ms.readRecord("proj-1", "dan")
	if err != nil {
		t.Fatal(err)
	}
	if reread.Role != domain.RoleOwner {
		t.Errorf("disk role: %q want owner", reread.Role)
	}
}

func TestMembershipStore_GrantRejectsInvalidRole(t *testing.T) {
	ms, err := NewMembershipStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewMembershipStore: %v", err)
	}
	_, err = ms.GrantMembership(context.Background(), &MembershipRecord{
		UserID:    "u",
		ProjectID: "p",
		Role:      "godmode",
	})
	if err == nil {
		t.Fatal("expected invalid-role error")
	}
}

func TestMembershipStore_Revoke(t *testing.T) {
	ms, err := NewMembershipStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewMembershipStore: %v", err)
	}
	ctx := context.Background()
	if _, err := ms.GrantMembership(ctx, &MembershipRecord{
		UserID: "dan", ProjectID: "proj-1", Role: domain.RoleViewer,
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := ms.RevokeMembership(ctx, "proj-1", "dan")
	if err != nil || !removed {
		t.Fatalf("RevokeMembership: removed=%v err=%v", removed, err)
	}
	members, err := ms.ListMembersOfProject("proj-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Errorf("expected empty members, got %+v", members)
	}

	// Idempotent — second revoke is a no-op (false, nil).
	removed, err = ms.RevokeMembership(ctx, "proj-1", "dan")
	if err != nil {
		t.Fatalf("idempotent revoke: %v", err)
	}
	if removed {
		t.Error("second revoke should report removed=false")
	}
}

func TestMembershipStore_ListMembersOfMissingProjectIsEmpty(t *testing.T) {
	ms, err := NewMembershipStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewMembershipStore: %v", err)
	}
	members, err := ms.ListMembersOfProject("nope")
	if err != nil {
		t.Fatalf("ListMembersOfProject: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("expected empty, got %+v", members)
	}
}

func TestMembershipStore_ListMembershipsForUserAcrossProjects(t *testing.T) {
	ms, err := NewMembershipStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewMembershipStore: %v", err)
	}
	ctx := context.Background()
	grants := []struct {
		project string
		role    domain.Role
	}{
		{"proj-2", domain.RoleMember},
		{"proj-1", domain.RoleOwner},
		{"proj-3", domain.RoleViewer},
	}
	for _, g := range grants {
		if _, err := ms.GrantMembership(ctx, &MembershipRecord{
			UserID: "dan", ProjectID: g.project, Role: g.role,
		}); err != nil {
			t.Fatalf("grant %s: %v", g.project, err)
		}
	}
	// Grant a different user on proj-1 to make sure we don't pick them up.
	if _, err := ms.GrantMembership(ctx, &MembershipRecord{
		UserID: "alice", ProjectID: "proj-1", Role: domain.RoleMember,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := ms.ListMembershipsForUser("dan")
	if err != nil {
		t.Fatal(err)
	}
	wantProjects := []string{"proj-1", "proj-2", "proj-3"}
	if len(out) != len(wantProjects) {
		t.Fatalf("got %d memberships, want %d (%+v)", len(out), len(wantProjects), out)
	}
	for i, p := range wantProjects {
		if out[i].ProjectID != p {
			t.Errorf("out[%d].ProjectID=%q want %q", i, out[i].ProjectID, p)
		}
	}
}
