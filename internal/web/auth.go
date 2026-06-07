package web

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/auth"
	"tickets_please/internal/auth/providers"
	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	pages "tickets_please/internal/web/components/pages"
)

const (
	// userCookieName carries the authenticated User id. It COEXISTS with the
	// tp_sid agent cookie (W2-2) — the two are unified in W2-3. Signed with the
	// same server secret as tp_sid but a distinct purpose label.
	userCookieName    = "tp_user"
	userCookiePurpose = "tp-user-v1"
	// indefiniteCookieMaxAge is what "indefinite" resolves to: a ~10-year
	// cookie. (http.Cookie MaxAge==0 means a session cookie that dies on
	// browser close — the opposite of what we want — so indefinite is encoded
	// as a very long finite Max-Age instead.)
	indefiniteCookieMaxAge = 10 * 365 * 24 * 60 * 60

	// oauthStateCookie holds the short-lived signed CSRF state + redirect
	// target for an in-flight OAuth handshake. Per the ticket hint, state lives
	// in a cookie, not server memory, so any server instance can complete the
	// callback.
	oauthStateCookie  = "tp_oauth"
	oauthStatePurpose = "tp-oauth-v1"
	oauthStateMaxAge  = 600 // 10 minutes
)

// buildProviders constructs the configured OAuth providers from the auth config
// block. A provider is included only when both client_id and client_secret are
// present, so a half-filled config silently omits the broken provider rather
// than surfacing a dead button.
func buildProviders(cfg config.AuthConfig) map[string]auth.Provider {
	out := map[string]auth.Provider{}
	for name, pc := range cfg.Providers {
		if pc.ClientID == "" || pc.ClientSecret == "" {
			continue
		}
		switch name {
		case "github":
			out[name] = providers.NewGitHub(pc.ClientID, pc.ClientSecret)
		case "google":
			out[name] = providers.NewGoogle(pc.ClientID, pc.ClientSecret)
		}
	}
	return out
}

// providerNames returns the configured provider names sorted for deterministic
// rendering.
func (a *app) providerNames() []string {
	names := make([]string, 0, len(a.providers))
	for n := range a.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// handleLoginPage renders the sign-in page with one button per configured
// provider, carrying the (sanitized) ?next= target forward to each start link.
func (a *app) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	next := sanitizeNext(r.URL.Query().Get("next"))
	var buttons []pages.ProviderButton
	for _, name := range a.providerNames() {
		buttons = append(buttons, pages.ProviderButton{
			Name:     name,
			Label:    providerLabel(name),
			StartURL: "/auth/" + name + "/start?next=" + url.QueryEscape(next),
		})
	}
	a.renderer.RenderTemplPartial(w, r, pages.Login(buttons))
}

// handleAuthStart generates CSRF state, stashes it (plus the redirect target)
// in a signed short-lived cookie, and redirects to the provider's consent page.
func (a *app) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	prov, ok := a.providers[name]
	if !ok {
		a.renderer.RenderTemplError(w, r, http.StatusNotFound, errors.New("unknown auth provider"))
		return
	}
	next := sanitizeNext(r.URL.Query().Get("next"))
	state, err := randomHex(16)
	if err != nil {
		a.renderer.RenderTemplError(w, r, http.StatusInternalServerError, errors.New("auth: state: "+err.Error()))
		return
	}
	a.writeStateCookie(w, r, name, state, next)
	http.Redirect(w, r, prov.AuthorizeURL(state, a.callbackURL(r, name)), http.StatusSeeOther)
}

// handleAuthCallback verifies state, exchanges the code, upserts the user, sets
// the session cookie, and redirects to the original target.
func (a *app) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	prov, ok := a.providers[name]
	if !ok {
		a.renderer.RenderTemplError(w, r, http.StatusNotFound, errors.New("unknown auth provider"))
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		a.renderer.RenderTemplError(w, r, http.StatusBadRequest, errors.New("auth: provider returned error: "+e))
		return
	}

	cookieProvider, cookieState, target, ok := a.readStateCookie(r)
	a.clearStateCookie(w, r)
	if !ok || cookieProvider != name {
		a.renderer.RenderTemplError(w, r, http.StatusBadRequest, errors.New("auth: missing or invalid state"))
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(cookieState)) != 1 {
		a.renderer.RenderTemplError(w, r, http.StatusBadRequest, errors.New("auth: state mismatch"))
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		a.renderer.RenderTemplError(w, r, http.StatusBadRequest, errors.New("auth: missing code"))
		return
	}

	claims, err := prov.Exchange(r.Context(), code, a.callbackURL(r, name))
	if err != nil {
		a.deps.Logger.Warn("auth: exchange failed", "provider", name, "err", err)
		a.renderer.RenderTemplError(w, r, http.StatusBadGateway, errors.New("auth: token exchange failed"))
		return
	}

	userID, err := a.upsertUser(r.Context(), claims)
	if err != nil {
		a.deps.Logger.Error("auth: upsert user", "provider", name, "err", err)
		a.renderer.RenderTemplError(w, r, http.StatusInternalServerError, errors.New("auth: could not persist user"))
		return
	}

	// First-run owner promotion (W2-6): env override or first-login-wins.
	// Never blocks login — failures are logged inside.
	a.maybeBootstrapAdmin(r.Context(), claims, userID)

	a.writeUserCookie(w, r, userID)
	a.deps.Logger.Info("auth: login", "provider", name, "user_id", userID, "subject", claims.Subject)
	http.Redirect(w, r, sanitizeNext(target), http.StatusSeeOther)
}

// handleLogout clears the user session cookie and returns to the login page.
// The legacy tp_sid agent cookie is left intact (it's the localhost identity,
// not the authenticated-user identity).
func (a *app) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.clearUserCookie(w, r)
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

// upsertUser finds the user linked to the OAuth subject (or creates one) and
// refreshes profile fields + LastLoginAt. The whole find-then-write runs under
// the user store's global lock to avoid two concurrent first-logins racing to
// create duplicate users for the same subject.
func (a *app) upsertUser(ctx context.Context, claims *auth.Claims) (string, error) {
	us := a.deps.Service.UserStore
	if us == nil {
		return "", errors.New("user store not configured")
	}
	var userID string
	err := us.WithGlobalLock(ctx, func() error {
		rec, findErr := us.FindUserByOAuthSubject(claims.Provider, claims.Subject)
		if findErr != nil && !errors.Is(findErr, domain.ErrNotFound) {
			return findErr
		}
		now := time.Now().UTC()
		if errors.Is(findErr, domain.ErrNotFound) {
			sub := claims.Subject
			rec = &store.UserRecord{
				ID:          uuid.New().String(),
				Email:       claims.Email,
				DisplayName: claims.DisplayName,
				AvatarURL:   claims.AvatarURL,
				CreatedAt:   now,
				LastLoginAt: now,
			}
			switch claims.Provider {
			case "github":
				rec.GitHubLogin = &sub
			case "google":
				rec.GoogleSub = &sub
			}
		} else {
			rec.LastLoginAt = now
			if claims.Email != "" {
				rec.Email = claims.Email
			}
			if claims.DisplayName != "" {
				rec.DisplayName = claims.DisplayName
			}
			if claims.AvatarURL != "" {
				rec.AvatarURL = claims.AvatarURL
			}
		}
		userID = rec.ID
		return us.WriteUser(rec)
	})
	if err != nil {
		return "", err
	}
	return userID, nil
}

// --- cookie machinery -------------------------------------------------------

// writeUserCookie sets the signed tp_user cookie carrying the user id.
func (a *app) writeUserCookie(w http.ResponseWriter, r *http.Request, userID string) {
	value := userID + "." + base64.RawURLEncoding.EncodeToString(a.session.signWith(userCookiePurpose, userID))
	http.SetCookie(w, &http.Cookie{
		Name:     userCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   a.userCookieMaxAge(),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.secureCookies(r),
	})
}

// userCookieMaxAge returns the tp_user login cookie's Max-Age in seconds,
// driven by auth.session_max_age_hours: >0 → that many hours; <=0 (the
// default) → indefinite. This is the web sign-in window the user asked to make
// long-lived for the single-user homelab; it's independent of the MCP
// agent-session TTL.
func (a *app) userCookieMaxAge() int {
	if h := a.deps.Cfg.Auth.SessionMaxAgeHours; h > 0 {
		return h * 60 * 60
	}
	return indefiniteCookieMaxAge
}

// userIDFromCookie returns the user id from a valid tp_user cookie, or "" when
// it's absent, malformed, or signature-invalid. Exposed for W2-3's middleware
// and used by tests to assert the post-login authenticated state.
func (a *app) userIDFromCookie(r *http.Request) string {
	c, err := r.Cookie(userCookieName)
	if err != nil {
		return ""
	}
	id, sig, ok := strings.Cut(c.Value, ".")
	if !ok || id == "" {
		return ""
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return ""
	}
	if subtle.ConstantTimeCompare(gotSig, a.session.signWith(userCookiePurpose, id)) != 1 {
		return ""
	}
	return id
}

// clearUserCookie expires the tp_user cookie.
func (a *app) clearUserCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     userCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.secureCookies(r),
	})
}

// writeStateCookie persists provider|state|target signed for the callback.
func (a *app) writeStateCookie(w http.ResponseWriter, r *http.Request, provider, state, target string) {
	payload := provider + "|" + state + "|" + target
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	value := enc + "." + base64.RawURLEncoding.EncodeToString(a.session.signWith(oauthStatePurpose, enc))
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   oauthStateMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.secureCookies(r),
	})
}

// readStateCookie returns (provider, state, target, ok) from a valid state
// cookie.
func (a *app) readStateCookie(r *http.Request) (string, string, string, bool) {
	c, err := r.Cookie(oauthStateCookie)
	if err != nil {
		return "", "", "", false
	}
	enc, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return "", "", "", false
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", "", "", false
	}
	if subtle.ConstantTimeCompare(gotSig, a.session.signWith(oauthStatePurpose, enc)) != 1 {
		return "", "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", "", "", false
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// clearStateCookie expires the in-flight OAuth state cookie.
func (a *app) clearStateCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.secureCookies(r),
	})
}

// --- helpers ----------------------------------------------------------------

// callbackURL builds <base>/auth/<provider>/callback. base comes from
// auth.base_url when set, otherwise it's inferred from the request (dev mode).
func (a *app) callbackURL(r *http.Request, provider string) string {
	base := strings.TrimRight(a.deps.Cfg.Auth.BaseURL, "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base + "/auth/" + provider + "/callback"
}

// secureCookies reports whether auth cookies should carry the Secure attribute:
// true over real TLS or when base_url is https (TLS-terminating proxy).
func (a *app) secureCookies(r *http.Request) bool {
	return r.TLS != nil || strings.HasPrefix(a.deps.Cfg.Auth.BaseURL, "https://")
}

// signWith returns hmac(purpose|msg) using the session secret.
func (m *sessionManager) signWith(purpose, msg string) []byte {
	return hmacSig(m.secret, purpose, msg)
}

// sanitizeNext clamps a post-login redirect target to a same-site absolute
// path, defending against open redirects. Anything not a clean "/..." path
// falls back to "/".
func sanitizeNext(s string) string {
	if s == "" || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") {
		return "/"
	}
	return s
}

// providerLabel is the human-facing button label for a provider key.
func providerLabel(name string) string {
	switch name {
	case "github":
		return "GitHub"
	case "google":
		return "Google"
	default:
		if name == "" {
			return name
		}
		return strings.ToUpper(name[:1]) + name[1:]
	}
}
