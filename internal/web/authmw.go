package web

import (
	"context"
	"net/http"
	"net/url"

	"tickets_please/internal/auth"
	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
)

// authMiddleware enforces that a request carries a valid user session when auth
// is enabled (i.e. at least one OAuth provider is configured). It hydrates the
// user from the store and attaches it to the context via auth.WithUser. When
// auth is disabled it is a pass-through, preserving the localhost no-auth mode
// and keeping the existing test suite (which never logs in) green.
func (a *app) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.authEnabled {
			next.ServeHTTP(w, r)
			return
		}
		uid := a.userIDFromCookie(r)
		if uid == "" {
			a.redirectToLogin(w, r)
			return
		}
		rec, err := a.deps.Service.UserStore.ReadUser(uid)
		if err != nil {
			// Stale cookie (user deleted) or a store error → treat as logged
			// out: drop the cookie and bounce to login.
			a.clearUserCookie(w, r)
			a.redirectToLogin(w, r)
			return
		}
		ctx := auth.WithUser(r.Context(), rec.ToDomain())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// redirectToLogin sends the visitor to the login page with a ?next= back-link.
// htmx requests get an HX-Redirect header (a 303 body would be swallowed by the
// swap); everything else gets a normal 303.
func (a *app) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	loginURL := "/auth/login?next=" + url.QueryEscape(r.URL.RequestURI())
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", loginURL)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, loginURL, http.StatusSeeOther)
}

// requireSlugRole guards a /p/{slug}/... route: the authenticated user must
// hold at least `min` on the project named by the {slug} path value. No-op when
// auth is disabled.
func (a *app) requireSlugRole(min domain.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.authEnabled {
			next.ServeHTTP(w, r)
			return
		}
		proj, err := a.deps.Service.GetProject(r.Context(), r.PathValue("slug"))
		if err != nil {
			a.renderer.RenderTemplError(w, r, http.StatusNotFound, err)
			return
		}
		a.guardRole(w, r, proj.ID, min, next)
	})
}

// requireTicketRole guards a /tickets/{id}... route by resolving the ticket's
// project, then checking membership. No-op when auth is disabled.
func (a *app) requireTicketRole(min domain.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.authEnabled {
			next.ServeHTTP(w, r)
			return
		}
		tk, err := a.deps.Service.GetTicket(r.Context(), r.PathValue("id"))
		if err != nil {
			a.renderer.RenderTemplError(w, r, http.StatusNotFound, err)
			return
		}
		a.guardRole(w, r, tk.ProjectID, min, next)
	})
}

// guardRole is the shared membership check: 403 unless the ctx user holds at
// least `min` on projectID.
func (a *app) guardRole(w http.ResponseWriter, r *http.Request, projectID string, min domain.Role, next http.Handler) {
	user, ok := auth.UserFrom(r.Context())
	if !ok {
		// authMiddleware should have populated this; defend anyway.
		a.redirectToLogin(w, r)
		return
	}
	mem, err := a.deps.Service.MembershipStore.GetMembership(projectID, user.ID)
	if err != nil || !mem.Role.Satisfies(min) {
		a.renderer.RenderTemplError(w, r, http.StatusForbidden, errForbidden{})
		return
	}
	next.ServeHTTP(w, r)
}

type errForbidden struct{}

func (errForbidden) Error() string {
	return "forbidden: you don't have sufficient access to this project"
}

// csrfID returns the session identity that CSRF tokens are keyed on. With auth
// enabled and a user present it's the user id (stable across agent-cookie
// rotation, per the W2-3 reconciliation); otherwise it falls back to the legacy
// agent id so the localhost no-auth mode is unchanged.
func (a *app) csrfID(ctx context.Context) string {
	if a.authEnabled {
		if u, ok := auth.UserFrom(ctx); ok {
			return u.ID
		}
	}
	id, _ := svc.SessionIDFrom(ctx)
	return id
}
