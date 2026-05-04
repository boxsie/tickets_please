package svc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"tickets_please/internal/domain"
)

// touchDebounceWindow is the minimum interval between two LastSeenAt
// rewrites for the same agent. Keeps the audit trail and yaml writes quiet
// when an agent hammers the API.
const touchDebounceWindow = time.Minute

// requireSession is the in-process middleware every mutating Service method
// calls at its top. It validates the session id attached to the context,
// loads the *domain.Agent, attaches it via WithAgent, and best-effort touches
// the agent's LastSeenAt (rate-limited to once per minute per agent).
//
// Reads (Get*/List*/Search*) skip this — read-only methods don't require
// identity.
func (s *Service) requireSession(ctx context.Context) (context.Context, *domain.Agent, error) {
	id, ok := SessionIDFrom(ctx)
	if !ok {
		return ctx, nil, fmt.Errorf("%w: register an agent first", domain.ErrUnauthenticated)
	}
	rec, err := s.AgentStore.ReadAgent(id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ctx, nil, fmt.Errorf("%w: unknown session", domain.ErrUnauthenticated)
		}
		return ctx, nil, err
	}
	if rec.ExpiresAt.Before(time.Now()) {
		return ctx, nil, fmt.Errorf("%w: session expired; re-register", domain.ErrUnauthenticated)
	}
	a := rec.ToDomain()
	s.touchAgentDebounced(a.ID)
	return WithAgent(ctx, a), a, nil
}

// optionalSession is the auth-soft variant of requireSession: when a valid
// session is on the context it returns the agent (and an agent-bearing ctx),
// when there isn't one it returns (ctx, nil, nil). Callers MUST handle the
// nil-agent case — leaving created_by / committed_by empty, skipping the
// auto-commit (StageOp.Commit already no-ops when agent is nil), etc.
//
// Used by the rare mutations that need to work pre-auth. Today that's just
// CreateProject — the very first write a fresh repo ever does, where there's
// no project to be authorized against yet. Every other mutation calls
// requireSession.
func (s *Service) optionalSession(ctx context.Context) (context.Context, *domain.Agent, error) {
	id, ok := SessionIDFrom(ctx)
	if !ok {
		return ctx, nil, nil
	}
	rec, err := s.AgentStore.ReadAgent(id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ctx, nil, nil
		}
		return ctx, nil, err
	}
	if rec.ExpiresAt.Before(time.Now()) {
		return ctx, nil, nil
	}
	a := rec.ToDomain()
	s.touchAgentDebounced(a.ID)
	return WithAgent(ctx, a), a, nil
}

// touchAgentDebounced bumps LastSeenAt for the given agent id, but only if
// we haven't already written within touchDebounceWindow. Best-effort: a
// failed touch logs a warning and is dropped — touches are not on the
// critical path of any mutation.
func (s *Service) touchAgentDebounced(id string) {
	now := time.Now()
	s.touchMu.Lock()
	if last, ok := s.touchOnce[id]; ok && now.Sub(last) < touchDebounceWindow {
		s.touchMu.Unlock()
		return
	}
	s.touchOnce[id] = now
	s.touchMu.Unlock()

	rec, err := s.AgentStore.ReadAgent(id)
	if err != nil {
		s.Logger.Warn("touch agent: read failed", "agent_id", id, "err", err)
		return
	}
	rec.LastSeenAt = now
	if err := s.AgentStore.WriteAgentRecord(rec); err != nil {
		s.Logger.Warn("touch agent: write failed", "agent_id", id, "err", err)
	}
}
