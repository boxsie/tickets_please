package svc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/domain"
	"tickets_please/internal/eventbus"
	"tickets_please/internal/store"
)

// RegisterAgent creates a new session for an agent self-identifying with the
// supplied key and name. It is the entry point of the auth flow: it does NOT
// itself require an existing session.
//
// TTL handling: requestedTTL is capped by Cfg.AgentSessionMaxMinutes; a zero
// or negative requestedTTL falls back to Cfg.AgentSessionTTLMinutes. Returns
// domain.ErrAlreadyExists if another agent's active session already holds
// the same key.
//
// actingForUserID optionally binds the session to a registered user; when
// non-empty the user must already exist (else domain.ErrInvalidArgument) and
// the agent thereafter inherits that user's per-project membership for
// authorization. Empty means a plain key-only agent — the default.
func (s *Service) RegisterAgent(ctx context.Context, key, name string, metadata map[string]string, requestedTTL time.Duration, actingForUserID string) (string, time.Time, error) {
	if strings.TrimSpace(key) == "" {
		return "", time.Time{}, fmt.Errorf("%w: agent key required", domain.ErrInvalidArgument)
	}
	if strings.TrimSpace(name) == "" {
		return "", time.Time{}, fmt.Errorf("%w: agent name required", domain.ErrInvalidArgument)
	}

	var actingForPtr *string
	if af := strings.TrimSpace(actingForUserID); af != "" {
		if s.UserStore == nil {
			return "", time.Time{}, fmt.Errorf("%w: acting_for_user_id given but no user store configured", domain.ErrInvalidArgument)
		}
		if _, err := s.UserStore.ReadUser(af); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return "", time.Time{}, fmt.Errorf("%w: acting_for_user_id %q is not a registered user", domain.ErrInvalidArgument, af)
			}
			return "", time.Time{}, fmt.Errorf("validate acting_for user: %w", err)
		}
		actingForPtr = &af
	}

	defaultTTL := time.Duration(s.Cfg.AgentSessionTTLMinutes) * time.Minute
	maxTTL := time.Duration(s.Cfg.AgentSessionMaxMinutes) * time.Minute
	ttl := requestedTTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	if maxTTL > 0 && ttl > maxTTL {
		ttl = maxTTL
	}
	if ttl <= 0 {
		// Both config values are zero/unset — pick a sane fallback so we
		// never write an already-expired session.
		ttl = 60 * time.Minute
	}

	now := time.Now()
	rec := &store.AgentRecord{
		ID:              uuid.NewString(),
		Key:             key,
		Name:            name,
		Metadata:        metadata,
		ActingForUserID: actingForPtr,
		CreatedAt:       now,
		ExpiresAt:       now.Add(ttl),
		LastSeenAt:      now,
	}

	if err := s.AgentStore.RegisterAgent(ctx, rec); err != nil {
		return "", time.Time{}, err
	}

	s.publish(eventbus.Event{
		Kind:      eventbus.KindAgentRegistered,
		Topics:    []string{eventbus.TopicGlobalAgents, eventbus.TopicAgent(rec.ID)},
		AgentID:   rec.ID,
		AgentName: rec.Name,
		UserID:    derefStr(rec.ActingForUserID),
	})
	return rec.ID, rec.ExpiresAt, nil
}

// Heartbeat bumps LastSeenAt for the supplied session id without extending
// the TTL. Self-identifies via the session id arg, so it does NOT call
// requireSession. Per the SPEC, heartbeats skip the auto-commit path —
// they're too noisy for the audit trail.
func (s *Service) Heartbeat(ctx context.Context, sessionID string) (time.Time, error) {
	if sessionID == "" {
		return time.Time{}, fmt.Errorf("%w: session id required", domain.ErrInvalidArgument)
	}
	rec, err := s.AgentStore.ReadAgent(sessionID)
	if err != nil {
		return time.Time{}, err
	}
	if rec.ExpiresAt.Before(time.Now()) {
		return time.Time{}, fmt.Errorf("%w: session expired; re-register", domain.ErrUnauthenticated)
	}
	rec.LastSeenAt = time.Now()
	if err := s.AgentStore.WriteAgentRecord(rec); err != nil {
		return time.Time{}, err
	}
	return rec.ExpiresAt, nil
}

// GetAgent returns the hydrated domain.Agent for the given session id. Read
// only; does not require a session.
func (s *Service) GetAgent(ctx context.Context, id string) (*domain.Agent, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: agent id required", domain.ErrInvalidArgument)
	}
	rec, err := s.AgentStore.ReadAgent(id)
	if err != nil {
		return nil, err
	}
	a := rec.ToDomain()
	s.hydrateActingFor(a)
	return a, nil
}
