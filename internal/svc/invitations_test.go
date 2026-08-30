package svc

import (
	"context"
	"errors"
	"testing"
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// TestInvitations_CreateAcceptRoleChange walks the acceptance path: an owner
// creates an invite, a user accepts it (membership upserted), the token is
// one-use (consumed), and the owner can then change that member's role.
func TestInvitations_CreateAcceptRoleChange(t *testing.T) {
	s, _, _, slug := freshServiceWithProject(t)
	ctx := context.Background()
	projID := projectIDForSlug(t, s, slug)
	userID := writeTestUser(t, s, "u-invitee", "Invitee")

	// Owner creates a member invite.
	inv, err := s.CreateInvitation(ctx, slug, "invitee@example.com", domain.RoleMember, "u-owner")
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if inv.Token == "" || inv.ID == "" {
		t.Fatalf("invitation missing token/id: %+v", inv)
	}
	if inv.ExpiresAt.Before(time.Now()) {
		t.Errorf("invitation should not be pre-expired: %v", inv.ExpiresAt)
	}

	// It shows up as pending.
	pending, err := s.ListInvitations(ctx, slug)
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListInvitations = %d (%v), want 1", len(pending), err)
	}

	// Accept upserts the membership at the invite's role.
	accepted, err := s.AcceptInvitation(ctx, inv.Token, userID)
	if err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}
	if accepted.ProjectID != projID {
		t.Errorf("accepted invite project = %q, want %q", accepted.ProjectID, projID)
	}
	mem, err := s.MembershipStore.GetMembership(projID, userID)
	if err != nil || mem.Role != domain.RoleMember {
		t.Fatalf("membership after accept = %+v (%v), want role member", mem, err)
	}

	// Token is one-use: it's gone now.
	if _, err := s.AcceptInvitation(ctx, inv.Token, userID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second accept should be ErrNotFound, got %v", err)
	}
	if pend, _ := s.ListInvitations(ctx, slug); len(pend) != 0 {
		t.Errorf("invite should be consumed, %d still pending", len(pend))
	}

	// Owner changes the member's role.
	if err := s.SetMemberRole(ctx, slug, userID, domain.RoleViewer, "u-owner"); err != nil {
		t.Fatalf("SetMemberRole: %v", err)
	}
	mem, err = s.MembershipStore.GetMembership(projID, userID)
	if err != nil || mem.Role != domain.RoleViewer {
		t.Fatalf("membership after role change = %+v (%v), want viewer", mem, err)
	}

	// And can remove them.
	if err := s.RemoveMember(ctx, slug, userID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if _, err := s.MembershipStore.GetMembership(projID, userID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("membership should be gone after remove, got %v", err)
	}
}

// TestInvitations_AcceptExpired: an expired invite is rejected and cleaned up.
func TestInvitations_AcceptExpired(t *testing.T) {
	s, _, _, slug := freshServiceWithProject(t)
	ctx := context.Background()
	projID := projectIDForSlug(t, s, slug)
	userID := writeTestUser(t, s, "u-late", "Late")

	// Write an already-expired invite directly through the store.
	past := time.Now().Add(-time.Hour).UTC()
	if _, err := s.InvitationStore.CreateInvitation(ctx, &store.InvitationRecord{
		ID:        "inv-expired",
		ProjectID: projID,
		Role:      domain.RoleMember,
		Token:     "stale-token",
		CreatedAt: past.Add(-7 * 24 * time.Hour),
		ExpiresAt: past,
	}); err != nil {
		t.Fatalf("seed expired invite: %v", err)
	}

	if _, err := s.AcceptInvitation(ctx, "stale-token", userID); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expired accept should be ErrInvalidArgument, got %v", err)
	}
	// No membership granted, and the stale invite was cleaned up.
	if _, err := s.MembershipStore.GetMembership(projID, userID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expired accept must not grant membership, got %v", err)
	}
	if pend, _ := s.ListInvitations(ctx, slug); len(pend) != 0 {
		t.Errorf("expired invite should be deleted, %d pending", len(pend))
	}
}
