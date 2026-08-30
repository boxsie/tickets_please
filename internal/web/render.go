package web

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"tickets_please/internal/domain"
	"tickets_please/internal/web/components/layout"
	"tickets_please/internal/web/components/partials"
)

// PageOpts is the per-call slice of PageData a handler fills in. Title is the
// browser tab title; CurrentSlug drives the sidebar's active highlight; Body
// is retained for backward compatibility but ignored by the templ render path
// (templ components carry their own typed props).
type PageOpts struct {
	Title       string
	CurrentSlug string
	Body        any
}

// Chrome is the layout chrome the renderer assembles per request via its
// ChromeProvider. Templ pages read it through layout.Chrome (mirror type).
type Chrome struct {
	Projects        []*domain.Project
	AgentLabel      string
	Flash           *Flash
	CSRF            string
	ShowLocalBanner bool
	// URL is the request path (no query string). Used by the sidebar's
	// per-project nav to highlight the active item via suffix-match.
	URL string
}

// Flash is a one-shot user-facing message stored in a short-lived `tp_flash`
// cookie. Set via SetFlash before a redirect; the next page render reads and
// clears it. Kind is one of "success", "info", "error".
type Flash struct {
	Kind    string
	Message string
}

// ChromeProvider supplies the per-request chrome the renderer can't compute on
// its own. Wired by the app layer; tests pass nil to skip chrome (templates
// then see a zero-valued Chrome).
type ChromeProvider interface {
	Chrome(w http.ResponseWriter, r *http.Request) Chrome
}

// Renderer wraps templ rendering with per-request chrome assembly. There's no
// template parsing or caching any more — templ generates static Go code at
// build time and that code does its own buffering.
//
// dev is preserved as a field so callers can keep passing it through, but it
// no longer drives a hot-reload path: templ's own `templ generate --watch`
// fills that role during development.
type Renderer struct {
	dev    bool
	chrome ChromeProvider
}

// NewRenderer builds a Renderer with the supplied chrome provider. chrome may
// be nil; when nil, RenderTempl renders with a zero-valued Chrome (no sidebar
// projects, empty agent label) — useful in tests.
func NewRenderer(dev bool, chrome ChromeProvider) *Renderer {
	return &Renderer{dev: dev, chrome: chrome}
}

// RenderTempl renders a templ component wrapped in the templ layout, with
// per-request chrome assembled by the renderer's ChromeProvider. If the
// request carries `HX-Request: true`, only the page component is rendered —
// no chrome — matching the pre-templ Page contract.
func (r *Renderer) RenderTempl(w http.ResponseWriter, req *http.Request, opts PageOpts, page templ.Component) {
	var chrome Chrome
	if r.chrome != nil {
		chrome = r.chrome.Chrome(w, req)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if req.Header.Get("HX-Request") == "true" {
		if err := page.Render(req.Context(), w); err != nil {
			r.renderErrorFallback(w, http.StatusInternalServerError, fmt.Errorf("render templ page (hx): %w", err))
		}
		return
	}

	data := layout.PageData{
		Title:       opts.Title,
		CurrentSlug: opts.CurrentSlug,
		Chrome:      chromeToLayout(chrome),
	}
	// templ.WithChildren attaches the page component as the layout's
	// `{ children... }` slot — Layout renders the chrome around it.
	ctx := templ.WithChildren(req.Context(), page)
	if err := layout.Layout(data).Render(ctx, w); err != nil {
		r.renderErrorFallback(w, http.StatusInternalServerError, fmt.Errorf("render templ layout: %w", err))
	}
}

// RenderTemplPartial renders a single templ component with no layout. Used
// for htmx partial swaps (e.g. the sidebar refresh endpoint) — the response
// replaces a fragment of the page, so chrome would double-render.
func (r *Renderer) RenderTemplPartial(w http.ResponseWriter, req *http.Request, comp templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := comp.Render(req.Context(), w); err != nil {
		r.renderErrorFallback(w, http.StatusInternalServerError, fmt.Errorf("render templ partial: %w", err))
	}
}

// chromeToLayout converts the renderer's private web.Chrome into the
// layout-package mirror. The mirror exists so the templ chrome can name its
// payload type without importing web (which would cycle web→layout→web).
func chromeToLayout(c Chrome) layout.Chrome {
	out := layout.Chrome{
		Projects:        c.Projects,
		AgentLabel:      c.AgentLabel,
		CSRF:            c.CSRF,
		ShowLocalBanner: c.ShowLocalBanner,
		URL:             c.URL,
	}
	if c.Flash != nil {
		out.Flash = &layout.Flash{Kind: c.Flash.Kind, Message: c.Flash.Message}
	}
	return out
}

// LayoutPageData builds a layout.PageData from the renderer's chrome plus the
// caller-supplied opts. Exposed (alongside chromeToLayout) so handlers that
// need to drive a templ partial with the same chrome shape RenderTempl would
// build — e.g. handleSidebarPartial — can do so without re-implementing the
// conversion.
func (r *Renderer) LayoutPageData(w http.ResponseWriter, req *http.Request, opts PageOpts) layout.PageData {
	var chrome Chrome
	if r.chrome != nil {
		chrome = r.chrome.Chrome(w, req)
	}
	return layout.PageData{
		Title:       opts.Title,
		CurrentSlug: opts.CurrentSlug,
		Chrome:      chromeToLayout(chrome),
	}
}

// RenderTemplError writes the supplied status, then renders partials.Error as
// the body. Use 422 for hard-rule violations (e.g. svc.MoveTicket with
// target=done), 404 for not-found, etc. The error's message goes into the
// partial's body.
func (r *Renderer) RenderTemplError(w http.ResponseWriter, req *http.Request, status int, err error) {
	w.WriteHeader(status)
	r.RenderTemplPartial(w, req, partials.Error(partials.ErrorProps{
		Status:  status,
		Message: err.Error(),
	}))
}

// renderErrorFallback is the last-resort error path when even the templ
// layer can't render the partial. Plaintext, no template lookup.
func (r *Renderer) renderErrorFallback(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// WriteHeader is a no-op if already called; that's fine.
	w.WriteHeader(status)
	var msg string
	var renderable interface{ Error() string }
	if errors.As(err, &renderable) {
		msg = renderable.Error()
	} else {
		msg = "internal error"
	}
	_, _ = fmt.Fprintf(w, "%d: %s\n", status, msg)
}

// --- Flash cookie -----------------------------------------------------------

const flashCookieName = "tp_flash"

// SetFlash stores a one-shot message in the tp_flash cookie. The next page
// render reads it and clears the cookie. Future tickets call this before a
// redirect (e.g. "Project created.").
func SetFlash(w http.ResponseWriter, r *http.Request, kind, message string) {
	raw, err := json.Marshal(Flash{Kind: kind, Message: message})
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

// readAndClearFlash decodes and consumes the tp_flash cookie, returning nil
// when no flash is set. Always emits a clearing Set-Cookie when one was
// present (even on decode failure) so a malformed cookie doesn't stick.
func readAndClearFlash(w http.ResponseWriter, r *http.Request) *Flash {
	c, err := r.Cookie(flashCookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	defer clearFlashCookie(w, r)
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	var f Flash
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	return &f
}

func clearFlashCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

// --- Localhost-banner host detection ---------------------------------------

// isLoopbackHost reports whether the request host is a loopback address or
// the literal "localhost". Used by the layout's localhost-only banner: when
// the user is hitting the UI from a non-loopback host (e.g. they exposed the
// port over Tailscale or a reverse proxy), we surface a no-auth warning.
//
// Reverse proxies that rewrite Host can defeat the check — that's fine; the
// banner is informational, not a security boundary.
func isLoopbackHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	// IPv6 literal in Host comes wrapped in [] which SplitHostPort strips.
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}
