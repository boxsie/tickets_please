package svc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// invitationTTL is how long a pending invite stays valid after creation.
const invitationTTL = 7 * 24 * time.Hour

// MemberView is a project membership joined with the inviter-facing identity
// fields from the user registry, for rendering the members table. DisplayName
// degrades to an id stub when the user record is missing.
type MemberView struct {
	UserID      string
	DisplayName string
	Email       string
	Role        domain.Role
	GrantedAt   time.Time
}

// ListProjectMembers returns the project's memberships hydrated with user
// display name + email, sorted by the underlying store (UserID order).
func (s *Service) ListProjectMembers(ctx context.Context, projectIDOrSlug string) ([]MemberView, error) {
	proj, err := s.GetProject(ctx, projectIDOrSlug)
	if err != nil {
		return nil, err
	}
	recs, err := s.MembershipStore.ListMembersOfProject(proj.ID)
	if err != nil {
		return nil, err
	}
	out := make([]MemberView, 0, len(recs))
	for _, m := range recs {
		v := MemberView{
			UserID:    m.UserID,
			Role:      m.Role,
			GrantedAt: m.GrantedAt,
		}
		if s.UserStore != nil {
			if u, err := s.UserStore.ReadUser(m.UserID); err == nil && u != nil {
				v.DisplayName = u.DisplayName
				v.Email = u.Email
			}
		}
		if v.DisplayName == "" {
			v.DisplayName = shortIDStub(m.UserID)
		}
		out = append(out, v)
	}
	return out, nil
}

// ListInvitations returns the project's pending invitations.
func (s *Service) ListInvitations(ctx context.Context, projectIDOrSlug string) ([]*domain.Invitation, error) {
	proj, err := s.GetProject(ctx, projectIDOrSlug)
	if err != nil {
		return nil, err
	}
	recs, err := s.InvitationStore.ListInvitationsForProject(proj.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Invitation, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ToDomain())
	}
	return out, nil
}

// CreateInvitation mints a one-use token + id, sets a 7-day expiry, and writes
// the pending invite. Email is optional metadata (the inline-link flow doesn't
// enforce it). createdBy is the inviting user's id.
func (s *Service) CreateInvitation(ctx context.Context, projectIDOrSlug, email string, role domain.Role, createdBy string) (*domain.Invitation, error) {
	proj, err := s.GetProject(ctx, projectIDOrSlug)
	if err != nil {
		return nil, err
	}
	if !roleValid(role) {
		return nil, fmt.Errorf("%w: invalid role %q", domain.ErrInvalidArgument, role)
	}
	token, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("invitation token: %w", err)
	}
	now := time.Now().UTC()
	rec := &store.InvitationRecord{
		ID:        uuid.NewString(),
		ProjectID: proj.ID,
		Email:     email,
		Role:      role,
		Token:     token,
		CreatedBy: createdBy,
		CreatedAt: now,
		ExpiresAt: now.Add(invitationTTL),
	}
	if _, err := s.InvitationStore.CreateInvitation(ctx, rec); err != nil {
		return nil, err
	}
	return rec.ToDomain(), nil
}

// AcceptInvitation consumes a token: it grants the user the invite's role on
// the project, then deletes the invite (one-use). An expired invite is deleted
// and rejected. Returns the (now-consumed) invitation so the caller can resolve
// the project to redirect to.
func (s *Service) AcceptInvitation(ctx context.Context, token, userID string) (*domain.Invitation, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: a logged-in user is required to accept an invitation", domain.ErrInvalidArgument)
	}
	rec, err := s.InvitationStore.FindByToken(token)
	if err != nil {
		return nil, err
	}
	inv := rec.ToDomain()
	if inv.Expired(time.Now().UTC()) {
		_ = s.InvitationStore.DeleteInvitation(ctx, inv.ProjectID, inv.ID)
		return nil, fmt.Errorf("%w: this invitation has expired", domain.ErrInvalidArgument)
	}
	if _, err := s.MembershipStore.GrantMembership(ctx, &store.MembershipRecord{
		UserID:    userID,
		ProjectID: inv.ProjectID,
		Role:      inv.Role,
		GrantedBy: inv.CreatedBy,
		GrantedAt: time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	if err := s.InvitationStore.DeleteInvitation(ctx, inv.ProjectID, inv.ID); err != nil {
		return nil, err
	}
	return inv, nil
}

// CancelInvitation deletes a pending invite by id (owner cancelling before the
// invitee accepts).
func (s *Service) CancelInvitation(ctx context.Context, projectIDOrSlug, invitationID string) error {
	proj, err := s.GetProject(ctx, projectIDOrSlug)
	if err != nil {
		return err
	}
	return s.InvitationStore.DeleteInvitation(ctx, proj.ID, invitationID)
}

// SetMemberRole upserts a member's role on the project (owner changing a
// member's level). grantedBy is the acting owner's user id.
func (s *Service) SetMemberRole(ctx context.Context, projectIDOrSlug, userID string, role domain.Role, grantedBy string) error {
	proj, err := s.GetProject(ctx, projectIDOrSlug)
	if err != nil {
		return err
	}
	if !roleValid(role) {
		return fmt.Errorf("%w: invalid role %q", domain.ErrInvalidArgument, role)
	}
	_, err = s.MembershipStore.GrantMembership(ctx, &store.MembershipRecord{
		UserID:    userID,
		ProjectID: proj.ID,
		Role:      role,
		GrantedBy: grantedBy,
		GrantedAt: time.Now().UTC(),
	})
	return err
}

// RemoveMember revokes a user's membership on the project.
func (s *Service) RemoveMember(ctx context.Context, projectIDOrSlug, userID string) error {
	proj, err := s.GetProject(ctx, projectIDOrSlug)
	if err != nil {
		return err
	}
	_, err = s.MembershipStore.RevokeMembership(ctx, proj.ID, userID)
	return err
}

func roleValid(r domain.Role) bool {
	switch r {
	case domain.RoleOwner, domain.RoleMember, domain.RoleViewer:
		return true
	}
	return false
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func shortIDStub(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
