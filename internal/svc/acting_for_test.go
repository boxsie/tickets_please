package svc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// writeTestUser inserts a user into the central UserStore and returns its id.
func writeTestUser(t *testing.T, s *Service, id, display string) string {
	t.Helper()
	if err := s.UserStore.WriteUser(&store.UserRecord{
		ID:          id,
		DisplayName: display,
		CreatedAt:   time.Now().UTC(),
		LastLoginAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}
	return id
}

// grantTestMembership grants role to user on project.
func grantTestMembership(t *testing.T, s *Service, projectID, userID string, role domain.Role) {
	t.Helper()
	if _, err := s.MembershipStore.GrantMembership(context.Background(), &store.MembershipRecord{
		UserID:    userID,
		ProjectID: projectID,
		Role:      role,
		GrantedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("GrantMembership: %v", err)
	}
}

func projectIDForSlug(t *testing.T, s *Service, slug string) string {
	t.Helper()
	projects, err := s.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range projects {
		if p.Slug == slug {
			return p.ID
		}
	}
	t.Fatalf("project %q not found", slug)
	return ""
}

// actingForCtx registers an agent bound to actingForUserID and returns a
// session context plus the hydrated agent.
func actingForCtx(t *testing.T, s *Service, actingForUserID string) (context.Context, *domain.Agent) {
	t.Helper()
	ctx := context.Background()
	id, _, err := s.RegisterAgent(ctx, "claude:af-"+actingForUserID, "Claude", nil, 0, actingForUserID)
	if err != nil {
		t.Fatalf("RegisterAgent acting-for: %v", err)
	}
	a, err := s.GetAgent(ctx, id)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	return WithSessionID(ctx, id), a
}

// TestRegisterAgent_ActingForUnknownUserRejected: acting_for must name an
// existing user.
func TestRegisterAgent_ActingForUnknownUserRejected(t *testing.T) {
	s := freshService(t)
	_, _, err := s.RegisterAgent(context.Background(), "k", "n", nil, 0, "ghost-user")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for unknown acting-for user, got %v", err)
	}
}

// TestRegisterAgent_ActingForHydratesDisplayName: GetAgent enriches ActingFor
// with the user's display name (the record layer can only carry the id).
func TestRegisterAgent_ActingForHydratesDisplayName(t *testing.T) {
	s := freshService(t)
	writeTestUser(t, s, "u-dan", "Dan")
	id, _, err := s.RegisterAgent(context.Background(), "claude:x", "Claude", nil, 0, "u-dan")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	a, err := s.GetAgent(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if a.ActingFor == nil || a.ActingFor.UserID != "u-dan" || a.ActingFor.DisplayName != "Dan" {
		t.Fatalf("ActingFor not hydrated: %+v", a.ActingFor)
	}
}

// TestActingFor_NoMembershipRejected: an acting-for agent whose user has no
// membership on the project cannot mutate it.
func TestActingFor_NoMembershipRejected(t *testing.T) {
	s, _, _, slug := freshServiceWithProject(t)
	writeTestUser(t, s, "u-nobody", "Nobody")
	afCtx, _ := actingForCtx(t, s, "u-nobody")

	_, err := s.CreateTicket(afCtx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug,
		Title:           "should be blocked",
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for no-membership acting-for, got %v", err)
	}
}

// TestActingFor_WithMembershipAllowedAndRecorded: a member can create, and the
// ticket records created_for both in memory and on disk.
func TestActingFor_WithMembershipAllowedAndRecorded(t *testing.T) {
	s, _, _, slug := freshServiceWithProject(t)
	pid := projectIDForSlug(t, s, slug)
	writeTestUser(t, s, "u-dan", "Dan")
	grantTestMembership(t, s, pid, "u-dan", domain.RoleMember)
	afCtx, _ := actingForCtx(t, s, "u-dan")

	tk, err := s.CreateTicket(afCtx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug,
		Title:           "acting ticket",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if tk.CreatedFor == nil || tk.CreatedFor.UserID != "u-dan" {
		t.Fatalf("expected CreatedFor u-dan, got %+v", tk.CreatedFor)
	}
	if tk.CreatedFor.DisplayName != "Dan" {
		t.Fatalf("expected CreatedFor display Dan, got %q", tk.CreatedFor.DisplayName)
	}

	// On-disk persistence: ticket.yaml carries created_for.
	matches, _ := filepath.Glob(filepath.Join(s.Store.Root, "tickets", "*", "ticket.yaml"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 ticket.yaml, got %d", len(matches))
	}
	rec := &store.TicketRecord{}
	if err := store.ReadYAML(matches[0], rec); err != nil {
		t.Fatalf("ReadYAML: %v", err)
	}
	if rec.CreatedForUserID == nil || *rec.CreatedForUserID != "u-dan" {
		t.Fatalf("created_for not persisted: %+v", rec.CreatedForUserID)
	}
}

// TestActingFor_ViewerRejectedOnWrite: viewers are read-only — an acting-for
// agent inheriting a viewer role cannot mutate.
func TestActingFor_ViewerRejectedOnWrite(t *testing.T) {
	s, _, _, slug := freshServiceWithProject(t)
	pid := projectIDForSlug(t, s, slug)
	writeTestUser(t, s, "u-view", "Viewer")
	grantTestMembership(t, s, pid, "u-view", domain.RoleViewer)
	afCtx, _ := actingForCtx(t, s, "u-view")

	_, err := s.CreateTicket(afCtx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug,
		Title:           "viewer blocked",
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for viewer write, got %v", err)
	}
}

// TestActingFor_CompletedForRecorded: completing as an acting-for owner stamps
// completed_for, and a comment authored along the way records author_for.
func TestActingFor_CompletedForRecorded(t *testing.T) {
	s, _, _, slug := freshServiceWithProject(t)
	pid := projectIDForSlug(t, s, slug)
	writeTestUser(t, s, "u-owner", "Olivia")
	grantTestMembership(t, s, pid, "u-owner", domain.RoleOwner)
	afCtx, _ := actingForCtx(t, s, "u-owner")

	tk, err := s.CreateTicket(afCtx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug,
		Title:           "complete me",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	cm, err := s.CreateComment(afCtx, tk.ID, "working on it")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if cm.AuthorFor == nil || cm.AuthorFor.UserID != "u-owner" {
		t.Fatalf("expected comment AuthorFor u-owner, got %+v", cm.AuthorFor)
	}
	done, err := s.CompleteTicket(afCtx, tk.ID, "tested", "did work", "learned a lot here")
	if err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}
	if done.CompletedFor == nil || done.CompletedFor.UserID != "u-owner" {
		t.Fatalf("expected CompletedFor u-owner, got %+v", done.CompletedFor)
	}
}

// TestActingFor_CreatedForHydratesOnColdLoad: after evicting the project from
// cache, GetTicket reloads from disk and the cache's lookupUserRef enriches
// created_for with the user's display name (not just the persisted id).
func TestActingFor_CreatedForHydratesOnColdLoad(t *testing.T) {
	s, _, _, slug := freshServiceWithProject(t)
	pid := projectIDForSlug(t, s, slug)
	writeTestUser(t, s, "u-dan", "Dan")
	grantTestMembership(t, s, pid, "u-dan", domain.RoleMember)
	afCtx, _ := actingForCtx(t, s, "u-dan")

	tk, err := s.CreateTicket(afCtx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug,
		Title:           "cold load",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	// Drop the in-memory copy so the next read hydrates from disk.
	s.Cache.Invalidate(slug)

	got, err := s.GetTicket(context.Background(), tk.ID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.CreatedFor == nil || got.CreatedFor.UserID != "u-dan" || got.CreatedFor.DisplayName != "Dan" {
		t.Fatalf("cold-load CreatedFor not hydrated: %+v", got.CreatedFor)
	}
}

// TestKeyOnlyAgent_UnrestrictedAndNoCreatedFor: a plain key-only agent (no
// acting_for) keeps today's behaviour — no membership required, no created_for.
func TestKeyOnlyAgent_UnrestrictedAndNoCreatedFor(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	// ctx is the plain authed agent from the fixture — no acting_for, and no
	// membership anywhere.
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug,
		Title:           "key only",
	})
	if err != nil {
		t.Fatalf("key-only CreateTicket should succeed unchanged, got %v", err)
	}
	if tk.CreatedFor != nil {
		t.Fatalf("key-only ticket should have nil CreatedFor, got %+v", tk.CreatedFor)
	}
}
