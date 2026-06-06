package web

import (
	"context"
	"net/http"
	"sort"

	"tickets_please/internal/auth"
	"tickets_please/internal/domain"
)

// Mount wires the web UI routes onto an existing http.ServeMux. Designed to
// share the mux with /mcp and /healthz that runServe already attaches —
// route paths here ("/", "/static/", "/p/...") don't collide.
//
// Every non-static route runs through the session middleware so handlers can
// pull the agent id off the context via svc.SessionIDFrom for downstream
// Service mutations. POST routes additionally run through the CSRF middleware
// (which calls r.ParseForm under the hood, so handlers can read r.Form.Get
// without touching ParseForm themselves).
//
// Route patterns use Go 1.22+ method+path matching: "GET /p/{$}" is the
// exact /p path (no trailing children), "{slug}" is a single-segment
// wildcard exposed via r.PathValue("slug"). Literal segments (/p/new,
// /p/load) take precedence over the wildcard, so the /p/{slug} handlers
// don't shadow the new/load forms.
func Mount(mux *http.ServeMux, deps Deps) {
	a := newApp(deps)

	// Static assets: served straight off the embedded (or on-disk in dev) FS.
	// No session middleware — assets don't need identity.
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(staticFS(deps.Dev))))

	// Route wrappers (W2-3). Base chain: session (agent attribution) → auth
	// (hydrate the logged-in user) → optional per-project role guard → CSRF
	// (POST only) → handler. Auth/role enforcement is a no-op unless OAuth is
	// configured, preserving the localhost no-auth mode.
	sess := a.session.middleware
	// authed: any logged-in user, no per-project role requirement.
	authed := func(h http.HandlerFunc) http.Handler {
		return sess(a.authMiddleware(http.HandlerFunc(a.withCSRF(h))))
	}
	// pub: public route — session + CSRF, but no login required.
	pub := func(h http.HandlerFunc) http.Handler {
		return sess(http.HandlerFunc(a.withCSRF(h)))
	}
	// slugRole: /p/{slug}/... guarded by a minimum role on that project.
	slugRole := func(min domain.Role, h http.HandlerFunc) http.Handler {
		return sess(a.authMiddleware(a.requireSlugRole(min, http.HandlerFunc(a.withCSRF(h)))))
	}
	// tktRole: /tickets/{id}... guarded by a minimum role on the ticket's project.
	tktRole := func(min domain.Role, h http.HandlerFunc) http.Handler {
		return sess(a.authMiddleware(a.requireTicketRole(min, http.HandlerFunc(a.withCSRF(h)))))
	}
	// wrap is an alias for authed so the non-slug routes below read cleanly.
	wrap := authed

	// Auth (W2-2). Run through the same session wrap so the legacy tp_sid
	// agent cookie + CSRF context still exist (withCSRF only enforces on
	// POST, so the GET login/start/callback routes are effectively public;
	// logout is the one POST and gets CSRF). OAuth state is carried in its
	// own signed short-lived cookie, not the CSRF token.
	mux.Handle("GET /auth/login", pub(a.handleLoginPage))
	mux.Handle("GET /auth/{provider}/start", pub(a.handleAuthStart))
	mux.Handle("GET /auth/{provider}/callback", pub(a.handleAuthCallback))
	mux.Handle("POST /auth/logout", wrap(a.handleLogout))

	// Invitation accept (#077). Lives at /invite/{token} rather than under
	// /auth/ to avoid colliding with the /auth/{provider}/... wildcard. `authed`
	// bounces an anonymous visitor to login with ?next= back here, so they
	// return to this URL once signed in; then the handler consumes the token
	// and grants membership.
	mux.Handle("GET /invite/{token}", authed(a.handleInviteAccept))

	// Sidebar swap endpoint: returns just the <aside id="sidebar"> fragment.
	// Wired by templates/partials/sidebar.tmpl's hx-get; triggered on the
	// body-scoped sidebar-refresh event that POST handlers emit via
	// HX-Trigger when the project set changes.
	mux.Handle("GET /_partials/sidebar", wrap(a.handleSidebarPartial))

	// Note: /p (no trailing slash) — matches exact path /p without
	// triggering ServeMux's `/p/` redirect behaviour. Tests and humans both
	// hit `/p` directly.
	mux.Handle("GET /p", wrap(a.handleProjectsIndex))
	mux.Handle("POST /p", wrap(a.handleProjectCreate))
	mux.Handle("GET /p/new", wrap(a.handleProjectNewForm))
	mux.Handle("GET /p/load", wrap(a.handleLoadProjectForm))
	mux.Handle("POST /p/load", wrap(a.handleLoadProjectMount))
	mux.Handle("GET /p/{slug}", slugRole(domain.RoleViewer, a.handleProjectDetail))
	mux.Handle("POST /p/{slug}", slugRole(domain.RoleOwner, a.handleProjectUpdate))
	mux.Handle("POST /p/{slug}/delete", slugRole(domain.RoleOwner, a.handleProjectDelete))
	mux.Handle("GET /p/{slug}/summary", slugRole(domain.RoleViewer, a.handleProjectSummaryView))
	mux.Handle("POST /p/{slug}/summary", slugRole(domain.RoleMember, a.handleProjectSummaryUpdate))
	mux.Handle("GET /p/{slug}/search", slugRole(domain.RoleViewer, a.handleProjectSearch))
	mux.Handle("GET /p/{slug}/settings", slugRole(domain.RoleOwner, a.handleProjectSettings))
	mux.Handle("POST /p/{slug}/settings", slugRole(domain.RoleOwner, a.handleProjectSettingsUpdate))
	mux.Handle("POST /p/{slug}/reembed", slugRole(domain.RoleOwner, a.handleProjectReembed))

	// Members + invitations (W2-4, #077). Owner-only management of who can see
	// and change the project. The accept route (below, near auth) is login-gated
	// instead — the invitee isn't a member yet.
	mux.Handle("GET /p/{slug}/members", slugRole(domain.RoleOwner, a.handleMembersIndex))
	mux.Handle("POST /p/{slug}/members/invite", slugRole(domain.RoleOwner, a.handleMemberInvite))
	mux.Handle("POST /p/{slug}/members/invitations/{id}/cancel", slugRole(domain.RoleOwner, a.handleInviteCancel))
	mux.Handle("POST /p/{slug}/members/{user_id}/role", slugRole(domain.RoleOwner, a.handleMemberRole))
	mux.Handle("POST /p/{slug}/members/{user_id}/remove", slugRole(domain.RoleOwner, a.handleMemberRemove))

	// Phase routes. Same wrap (session + CSRF on POST). Literal segments
	// (/phases, /new) take precedence over the {phase} wildcard.
	mux.Handle("GET /p/{slug}/phases", slugRole(domain.RoleViewer, a.handlePhasesIndex))
	mux.Handle("POST /p/{slug}/phases", slugRole(domain.RoleMember, a.handlePhaseCreate))
	mux.Handle("GET /p/{slug}/phases/new", slugRole(domain.RoleMember, a.handlePhaseNewForm))
	mux.Handle("GET /p/{slug}/phases/{phase}", slugRole(domain.RoleViewer, a.handlePhaseDetail))
	mux.Handle("POST /p/{slug}/phases/{phase}", slugRole(domain.RoleMember, a.handlePhaseUpdate))
	mux.Handle("GET /p/{slug}/phases/{phase}/edit", slugRole(domain.RoleMember, a.handlePhaseEditForm))
	mux.Handle("POST /p/{slug}/phases/{phase}/delete", slugRole(domain.RoleMember, a.handlePhaseDelete))
	mux.Handle("GET /p/{slug}/phases/{phase}/summary", slugRole(domain.RoleViewer, a.handlePhaseSummaryView))
	mux.Handle("POST /p/{slug}/phases/{phase}/summary", slugRole(domain.RoleMember, a.handlePhaseSummaryUpdate))

	// Cross-cutting: reassign a ticket between phases. /tickets/{id} is
	// owned by ticket 5; the assign-phase POST lives here under the phases
	// owner (ticket 4).
	mux.Handle("POST /tickets/{id}/assign-phase", tktRole(domain.RoleMember, a.handleAssignTicketToPhase))

	// Tickets: board, create form, create POST, detail, edit form, update,
	// move (comment-required), complete (3 textareas). All ticket-mutation
	// URLs accept an optional ?slug= hint to skip hostStoreForTicket.
	mux.Handle("GET /p/{slug}/board", slugRole(domain.RoleViewer, a.handleBoard))
	mux.Handle("GET /p/{slug}/tickets/new", slugRole(domain.RoleMember, a.handleTicketNewForm))
	mux.Handle("POST /p/{slug}/tickets", slugRole(domain.RoleMember, a.handleTicketCreate))
	mux.Handle("GET /tickets/{id}", tktRole(domain.RoleViewer, a.handleTicketDetail))
	mux.Handle("GET /tickets/{id}/edit", tktRole(domain.RoleMember, a.handleTicketEditForm))
	mux.Handle("POST /tickets/{id}", tktRole(domain.RoleMember, a.handleTicketUpdate))
	mux.Handle("POST /tickets/{id}/move", tktRole(domain.RoleMember, a.handleTicketMove))
	mux.Handle("POST /tickets/{id}/complete", tktRole(domain.RoleMember, a.handleTicketComplete))
	mux.Handle("POST /tickets/{id}/delete", tktRole(domain.RoleMember, a.handleTicketDelete))

	// Comments thread: list (htmx refresh) + create (htmx append).
	mux.Handle("GET /tickets/{id}/comments", tktRole(domain.RoleViewer, a.handleCommentsList))
	mux.Handle("POST /tickets/{id}/comments", tktRole(domain.RoleMember, a.handleCommentCreate))

	// Filesystem picker for /p/load. Read-only directory listing. JSON for
	// API clients, HTML partial for the htmx-driven /p/load picker.
	mux.Handle("GET /api/fs", wrap(a.handleFSBrowse))

	// Top-level server settings (W5-T2 of the per-project-embedders phase).
	// Edits server defaults that gate *new* projects + shared transport
	// (ollama_url, openai_api_key); also exposes the bulk Re-embed-all
	// button. Per-project /p/{slug}/settings (W5-T1) is a sibling region.
	mux.Handle("GET /settings", wrap(a.handleGlobalSettings))
	mux.Handle("POST /settings", wrap(a.handleGlobalSettingsUpdate))
	mux.Handle("POST /settings/reembed-all", wrap(a.handleReembedAll))

	// /logs renders the in-process slog ring buffer as a plain <pre> page.
	// Useful for poking at server activity from the browser without tailing
	// stderr — the same JSON records, mirrored via internal/log.RingHandler.
	mux.Handle("GET /logs", wrap(a.handleLogs))

	// /sse is the always-on realtime channel — Datastar subscribes here for
	// push-driven DOM patches. Unwrapped (no session/CSRF middleware): the
	// stream is GET-only, identity arrives via cookie if the subscriber
	// already has one, and W1 publishes to a single global topic with no
	// per-session scoping. handlers_sse.go closes the stream on context
	// cancel — the same lifecycle the http.Server gives every handler.
	mux.Handle("GET /sse", http.HandlerFunc(a.handleSSE))

	// Dev-only scaffolding: smoke routes for the templ + Tailwind + Datastar
	// migration. Gated on deps.Dev so production builds don't expose them.
	if deps.Dev {
		mux.Handle("GET /_dev/templ-hello", wrap(a.handleTemplHello))
		mux.Handle("GET /_dev/components", wrap(a.handleComponentsPlayground))
		// Bypass wrap (session + CSRF) for the dev SSE ping: it's a dev-only
		// fire-and-forget that publishes to the Hub. Datastar's @post action
		// doesn't carry the form-style _csrf hidden field, and CSRF on a
		// dev-only no-op buys nothing.
		mux.Handle("POST /_dev/sse-ping", http.HandlerFunc(a.handleSSEPing))
	}

	// Root: home handler. http.ServeMux's "/" pattern catches every path not
	// matched by a more specific handler, so the more-specific /p/* patterns
	// above preempt it.
	mux.Handle("/", authed(a.handleHome))
}

// app bundles the per-mount construction (renderer, session manager) so
// handler methods can hang off it without dragging Deps + Renderer + sessions
// through every signature. *app also satisfies ChromeProvider — the renderer
// calls back into it on every Page render to assemble per-request chrome.
type app struct {
	deps      Deps
	renderer  *Renderer
	session   *sessionManager
	providers map[string]auth.Provider
	// authEnabled flips on the W2-3 login + per-project role enforcement. It's
	// true exactly when at least one OAuth provider is configured; otherwise
	// the web UI runs in the legacy localhost-only no-auth mode.
	authEnabled bool
}

func newApp(deps Deps) *app {
	a := &app{deps: deps}
	a.session = newSessionManager(deps)
	a.providers = buildProviders(deps.Cfg.Auth)
	a.authEnabled = len(a.providers) > 0
	// Renderer holds a back-reference to a so it can fetch chrome (sidebar
	// projects, agent label, flash, csrf) per request.
	a.renderer = NewRenderer(deps.Dev, a)
	return a
}

// Chrome implements ChromeProvider. Called by the renderer on every Page
// render to assemble the layout chrome — sidebar project list, agent label,
// pending flash message, CSRF token, localhost-banner gate.
//
// ListProjects errors are logged and degrade to an empty sidebar rather than
// blowing up the whole page; a missing project list is annoying but
// recoverable, while a 500 on every navigation isn't.
func (a *app) Chrome(w http.ResponseWriter, r *http.Request) Chrome {
	projects := a.sidebarProjects(r.Context())
	csrf := ""
	if id := a.csrfID(r.Context()); id != "" {
		csrf = csrfToken(a.session.secret, id)
	}
	return Chrome{
		Projects:        projects,
		AgentLabel:      a.session.agentLabel(r.Context()),
		Flash:           readAndClearFlash(w, r),
		CSRF:            csrf,
		ShowLocalBanner: !isLoopbackHost(r.Host),
		URL:             r.URL.Path,
	}
}

// sidebarProjects fetches the project list for the sidebar, sorted by slug
// for stable rendering. Returns an empty slice (not nil) on error; a nil
// slice in templates renders identically but the explicit slice is friendlier
// to range over.
func (a *app) sidebarProjects(ctx context.Context) []*domain.Project {
	list, err := a.deps.Service.ListProjects(ctx)
	if err != nil {
		a.deps.Logger.Warn("chrome: list projects", "err", err)
		return []*domain.Project{}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Slug < list[j].Slug })
	return list
}
