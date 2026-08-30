package svc

import (
	"errors"
	"fmt"

	"tickets_please/internal/domain"
)

// hydrateActingFor fills in the DisplayName of an agent's ActingFor link from
// the UserStore. The record layer (AgentRecord.ToDomain) can only set the
// UserID — it has no store handle — so every svc surface that returns a
// *domain.Agent (requireSession, optionalSession, GetAgent) calls this to make
// the link renderable ("acting for <DisplayName>"). Best-effort: a missing or
// unreadable user leaves the id-only ref in place rather than failing the call.
func (s *Service) hydrateActingFor(a *domain.Agent) {
	if a == nil || a.ActingFor == nil || a.ActingFor.UserID == "" {
		return
	}
	if a.ActingFor.DisplayName != "" || s.UserStore == nil {
		return
	}
	if rec, err := s.UserStore.ReadUser(a.ActingFor.UserID); err == nil && rec != nil {
		a.ActingFor.DisplayName = rec.DisplayName
	}
}

// actingForUserID returns a pointer to the bound user id when the agent is
// acting for a user, else nil — the value persisted as the ticket's
// created_for / completed_for.
func actingForUserID(agent *domain.Agent) *string {
	if agent == nil || agent.ActingFor == nil || agent.ActingFor.UserID == "" {
		return nil
	}
	id := agent.ActingFor.UserID
	return &id
}

// actingForRef returns the UserRef to attach to a freshly-built in-memory
// domain.Ticket, mirroring what the cache hydrates on a later read so the
// optimistic in-cache copy matches the on-disk round-trip.
func actingForRef(agent *domain.Agent) *domain.UserRef {
	if agent == nil || agent.ActingFor == nil || agent.ActingFor.UserID == "" {
		return nil
	}
	r := *agent.ActingFor
	return &r
}

// authorizeActingFor enforces the acting-for membership contract for a mutation
// on the given project. For a plain key-only agent (ActingFor == nil) it is a
// no-op — those agents are authenticated by key and unrestricted, today's
// behaviour. For an acting-for agent it requires that the bound user holds a
// membership on the project; a `write` mutation additionally requires a role
// above viewer (viewers are read-only). On failure it returns domain.ErrForbidden.
//
// This is the single chokepoint the ticket calls for: "an agent can only mutate
// projects its bound user has access to." Mutating svc methods call it right
// after they resolve the target project id.
func (s *Service) authorizeActingFor(agent *domain.Agent, projectID string, write bool) error {
	if agent == nil || agent.ActingFor == nil {
		return nil
	}
	uid := agent.ActingFor.UserID
	if s.MembershipStore == nil {
		return fmt.Errorf("%w: acting for user %s but no membership store configured", domain.ErrForbidden, uid)
	}
	m, err := s.MembershipStore.GetMembership(projectID, uid)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("%w: user %s has no membership on this project", domain.ErrForbidden, uid)
		}
		return fmt.Errorf("check membership: %w", err)
	}
	if write && m.Role == domain.RoleViewer {
		return fmt.Errorf("%w: user %s is a viewer (read-only) on this project", domain.ErrForbidden, uid)
	}
	return nil
}
