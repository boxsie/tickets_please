package auth

import (
	"context"

	"tickets_please/internal/domain"
)

// ctxKey is the private context-key type for this package.
type ctxKey int

const userCtxKey ctxKey = iota

// WithUser returns a copy of ctx carrying the authenticated user. The web auth
// middleware sets this after hydrating the user from the session cookie;
// handlers and the renderer read it back via UserFrom.
func WithUser(ctx context.Context, u *domain.User) context.Context {
	return context.WithValue(ctx, userCtxKey, u)
}

// UserFrom returns the authenticated user attached to ctx, or (nil, false) when
// the request is unauthenticated (or auth is disabled).
func UserFrom(ctx context.Context) (*domain.User, bool) {
	u, ok := ctx.Value(userCtxKey).(*domain.User)
	return u, ok && u != nil
}
