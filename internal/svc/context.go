package svc

import (
	"context"

	"tickets_please/internal/domain"
)

// ctxKey is a private type so callers can't accidentally collide with our
// keys via untyped string lookups.
type ctxKey int

const (
	keySessionID ctxKey = iota
	keyAgent
)

// WithSessionID returns a child context carrying the agent session id. The
// MCP transport (T12) calls this before invoking each Service method.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keySessionID, id)
}

// SessionIDFrom extracts the session id installed by WithSessionID. The
// second return is false when no id was attached or the value is empty.
func SessionIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(keySessionID).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// WithAgent returns a child context carrying the resolved agent. The
// requireSession middleware installs this so handlers can read the agent
// without re-loading from the store.
func WithAgent(ctx context.Context, a *domain.Agent) context.Context {
	return context.WithValue(ctx, keyAgent, a)
}

// AgentFrom extracts the agent installed by WithAgent. The second return is
// false when no agent was attached.
func AgentFrom(ctx context.Context) (*domain.Agent, bool) {
	a, ok := ctx.Value(keyAgent).(*domain.Agent)
	if !ok || a == nil {
		return nil, false
	}
	return a, true
}
