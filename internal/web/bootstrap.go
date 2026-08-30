package web

import (
	"context"
	"os"
	"strings"
	"time"

	"tickets_please/internal/auth"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// bootstrapAdminEnv is the env var that names the OAuth identity to be granted
// owner of every project on login. Format: "<provider>:<identifier>" where
// identifier matches the claims subject OR email — so both "github:boxsie"
// (subject == login) and "google:dan@example.com" (email) work even though
// Google's stable subject is the opaque OIDC `sub`.
const bootstrapAdminEnv = "TICKETS_PLEASE_BOOTSTRAP_ADMIN"

// maybeBootstrapAdmin runs the first-run owner-promotion logic right after a
// successful OAuth upsert. Two triggers, checked in priority order:
//
//  1. TICKETS_PLEASE_BOOTSTRAP_ADMIN names this identity → grant owner of all
//     projects on every login (idempotent; survives new projects being added).
//  2. env unset AND this is the only user in the store → first-login-wins:
//     the very first human to authenticate owns everything.
//
// Either way the grant is a backfill across the current project list. Failures
// are logged but never block login — a missing membership is recoverable via
// the `grant-owner` CLI, a failed login is not.
func (a *app) maybeBootstrapAdmin(ctx context.Context, claims *auth.Claims, userID string) {
	spec := strings.TrimSpace(os.Getenv(bootstrapAdminEnv))

	var reason string
	switch {
	case spec != "" && bootstrapSpecMatches(spec, claims):
		reason = "env_override"
	case spec == "" && a.isOnlyUser(userID):
		reason = "first_login_wins"
	default:
		return
	}

	n, err := a.grantOwnerOfAllProjects(ctx, userID)
	if err != nil {
		a.deps.Logger.Error("auth: bootstrap admin grant failed",
			"reason", reason, "user_id", userID, "err", err)
		return
	}
	a.deps.Logger.Info("auth: bootstrap admin promoted",
		"reason", reason, "user_id", userID, "projects_granted", n,
		"subject", claims.Subject, "provider", claims.Provider)
}

// bootstrapSpecMatches reports whether the env spec "<provider>:<identifier>"
// names the just-authenticated identity. The identifier matches either the
// provider subject (GitHub login / Google sub) or the email, so operators can
// configure whichever is convenient.
func bootstrapSpecMatches(spec string, claims *auth.Claims) bool {
	provider, ident, ok := strings.Cut(spec, ":")
	if !ok {
		return false
	}
	provider = strings.TrimSpace(provider)
	ident = strings.TrimSpace(ident)
	if provider == "" || ident == "" || provider != claims.Provider {
		return false
	}
	return ident == claims.Subject || (claims.Email != "" && ident == claims.Email)
}

// isOnlyUser reports whether userID is the sole user in the store — the
// first-login-wins condition. Counting (rather than pre-checking emptiness
// before upsert) is robust: after upsertUser writes the new record, exactly one
// user existing means this login created the first account.
func (a *app) isOnlyUser(userID string) bool {
	us := a.deps.Service.UserStore
	if us == nil {
		return false
	}
	count := 0
	other := false
	err := us.WalkUsers(func(rec *store.UserRecord) error {
		count++
		if rec.ID != userID {
			other = true
		}
		return nil
	})
	if err != nil {
		a.deps.Logger.Warn("auth: bootstrap user-count walk failed", "err", err)
		return false
	}
	return count == 1 && !other
}

// grantOwnerOfAllProjects backfills an owner membership for userID across every
// project the service can see. Idempotent per project (GrantMembership no-ops a
// repeat same-role grant). GrantedBy is left empty to mark a system grant.
// Returns the number of projects processed.
func (a *app) grantOwnerOfAllProjects(ctx context.Context, userID string) (int, error) {
	projects, err := a.deps.Service.ListProjects(ctx)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	granted := 0
	for _, p := range projects {
		if _, err := a.deps.Service.MembershipStore.GrantMembership(ctx, &store.MembershipRecord{
			UserID:    userID,
			ProjectID: p.ID,
			Role:      domain.RoleOwner,
			GrantedAt: now,
		}); err != nil {
			return granted, err
		}
		granted++
	}
	return granted, nil
}
