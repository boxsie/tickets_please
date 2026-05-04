package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"

	"tickets_please/internal/domain"
)

// PageOpts is the per-call slice of PageData a handler fills in. Title is the
// browser tab title; CurrentSlug drives the sidebar's active highlight; Body
// carries page-specific data the page template reaches via `.Body`. Everything
// else (sidebar projects, agent label, flash, CSRF, localhost banner gate)
// comes from the renderer's ChromeProvider.
type PageOpts struct {
	Title       string
	CurrentSlug string
	Body        any
}

// Chrome is the layout chrome the renderer assembles per request via its
// ChromeProvider. Templates reach it through `.Chrome.*`.
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

// PageData is the full payload templates receive. Templates use `.Title`,
// `.CurrentSlug`, `.Chrome.*`, `.Body.*`.
type PageData struct {
	Title       string
	CurrentSlug string
	Chrome      Chrome
	Body        any
}

// Flash is a one-shot user-facing message stored in a short-lived `tp_flash`
// cookie. Set via SetFlash before a redirect; the next Page render reads and
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

// Renderer parses templates from a templatesFS and exposes Page (full
// _layout-wrapped HTML), Partial (chrome-less, for htmx swaps), and Error
// (status + partials/error.tmpl) helpers.
//
// In prod (dev=false) parsed templates are cached in-process. In dev mode
// every render reparses from disk so .tmpl edits show up on refresh.
type Renderer struct {
	fs     fs.FS
	dev    bool
	chrome ChromeProvider

	mu       sync.RWMutex
	pages    map[string]*template.Template // key: page name (e.g. "home")
	mains    map[string]*template.Template // page template parsed alone, for HX-Request "main" block fragments
	partials map[string]*template.Template // key: partial name (e.g. "error")
}

// NewRenderer builds a Renderer bound to the supplied templates FS. chrome may
// be nil; when nil, Page renders with a zero-valued Chrome (no sidebar
// projects, empty agent label) — useful in tests.
func NewRenderer(tplFS fs.FS, dev bool, chrome ChromeProvider) *Renderer {
	return &Renderer{
		fs:       tplFS,
		dev:      dev,
		chrome:   chrome,
		pages:    map[string]*template.Template{},
		mains:    map[string]*template.Template{},
		partials: map[string]*template.Template{},
	}
}

// Page renders pages/<name>.tmpl wrapped in _layout.tmpl, with the supplied
// opts. If the request carries `HX-Request: true`, Page renders just the
// page's "main" block standalone — no chrome — so an hx-boost or
// hx-swap=outerHTML on <main> doesn't drag the layout. The page's main block
// still receives the full PageData (so it can reference .Chrome.CSRF etc.),
// just rendered without the surrounding _layout.
func (r *Renderer) Page(w http.ResponseWriter, req *http.Request, name string, opts PageOpts) {
	var chrome Chrome
	if r.chrome != nil {
		chrome = r.chrome.Chrome(w, req)
	}
	data := PageData{
		Title:       opts.Title,
		CurrentSlug: opts.CurrentSlug,
		Chrome:      chrome,
		Body:        opts.Body,
	}

	if req.Header.Get("HX-Request") == "true" {
		t, err := r.loadMain(name)
		if err != nil {
			r.renderErrorFallback(w, http.StatusInternalServerError, fmt.Errorf("load page main %q: %w", name, err))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := t.ExecuteTemplate(w, "main", data); err != nil {
			r.renderErrorFallback(w, http.StatusInternalServerError, fmt.Errorf("execute %q main: %w", name, err))
		}
		return
	}

	t, err := r.loadPage(name)
	if err != nil {
		r.renderErrorFallback(w, http.StatusInternalServerError, fmt.Errorf("load page %q: %w", name, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "_layout.tmpl", data); err != nil {
		// Headers may already be flushed; nothing useful to do but log via
		// the http.Error fallback (which itself may noop on flushed writes).
		// Callers see the truncated response.
		r.renderErrorFallback(w, http.StatusInternalServerError, fmt.Errorf("execute %q: %w", name, err))
	}
}

// Partial renders partials/<name>.tmpl alone (no _layout). Used for htmx
// swaps — the response replaces a fragment of the page, so chrome would
// double-render.
func (r *Renderer) Partial(w http.ResponseWriter, _ *http.Request, name string, data any) {
	t, err := r.loadPartial(name)
	if err != nil {
		r.renderErrorFallback(w, http.StatusInternalServerError, fmt.Errorf("load partial %q: %w", name, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, name+".tmpl", data); err != nil {
		r.renderErrorFallback(w, http.StatusInternalServerError, fmt.Errorf("execute partial %q: %w", name, err))
	}
}

// Error renders partials/error.tmpl at the supplied status. Use 422 for
// hard-rule violations (e.g. svc.MoveTicket with target=done), 404 for
// not-found, etc. The error's message goes into the partial's body.
func (r *Renderer) Error(w http.ResponseWriter, req *http.Request, status int, err error) {
	w.WriteHeader(status)
	r.Partial(w, req, "error", errorData{
		Status:  status,
		Message: err.Error(),
	})
}

// errorData is the payload for partials/error.tmpl.
type errorData struct {
	Status  int
	Message string
}

func (r *Renderer) loadPage(name string) (*template.Template, error) {
	if !r.dev {
		r.mu.RLock()
		t, ok := r.pages[name]
		r.mu.RUnlock()
		if ok {
			return t, nil
		}
	}
	t, err := template.New("_layout.tmpl").
		Funcs(funcMap).
		ParseFS(r.fs,
			"_layout.tmpl",
			"_nav.tmpl",
			"pages/"+name+".tmpl",
			"partials/*.tmpl",
		)
	if err != nil {
		return nil, err
	}
	if !r.dev {
		r.mu.Lock()
		r.pages[name] = t
		r.mu.Unlock()
	}
	return t, nil
}

// loadMain parses pages/<name>.tmpl alone — no _layout, no nav/sidebar — so
// the "main" block can be rendered as a chrome-less fragment in response to
// HX-Request: true. Cached separately from full pages because the parse tree
// has different sibling templates available.
func (r *Renderer) loadMain(name string) (*template.Template, error) {
	if !r.dev {
		r.mu.RLock()
		t, ok := r.mains[name]
		r.mu.RUnlock()
		if ok {
			return t, nil
		}
	}
	t, err := template.New(name+".tmpl").
		Funcs(funcMap).
		ParseFS(r.fs, "pages/"+name+".tmpl")
	if err != nil {
		return nil, err
	}
	if !r.dev {
		r.mu.Lock()
		r.mains[name] = t
		r.mu.Unlock()
	}
	return t, nil
}

func (r *Renderer) loadPartial(name string) (*template.Template, error) {
	if !r.dev {
		r.mu.RLock()
		t, ok := r.partials[name]
		r.mu.RUnlock()
		if ok {
			return t, nil
		}
	}
	// Parse all partials together (not just the requested one) so a partial
	// can {{template "other.tmpl" ...}} into a sibling without a separate
	// loader. Cheap — a couple dozen tiny files at most.
	t, err := template.New(name+".tmpl").
		Funcs(funcMap).
		ParseFS(r.fs, "partials/*.tmpl")
	if err != nil {
		return nil, err
	}
	if !r.dev {
		r.mu.Lock()
		r.partials[name] = t
		r.mu.Unlock()
	}
	return t, nil
}

// renderErrorFallback is the last-resort error path when even the templating
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

// funcMap is the shared FuncMap available to every template. Kept tiny on
// purpose; later tickets add helpers as they need them.
var funcMap = template.FuncMap{
	// safeHTML lets templates emit pre-sanitised HTML (e.g. markdown-rendered
	// summary content). USE ONLY on values that have already been run through
	// a sanitiser — never on raw user input.
	"safeHTML": func(s string) template.HTML { return template.HTML(s) },
	// markdown converts a markdown string to safe HTML. Raw HTML in the
	// source is escaped (see internal/web/markdown.go for the rationale).
	"markdown": renderMarkdown,
	// mkTicketCard packages (ticket, projectSlug) for the ticket_card.tmpl
	// partial so the board template can pass a single struct instead of
	// piping multiple values through the template syntax.
	"mkTicketCard": func(t any, slug string) map[string]any {
		return map[string]any{"Ticket": t, "ProjectSlug": slug}
	},
	// mkAssignPhase builds the payload for assign_phase_form.tmpl.
	"mkAssignPhase": func(ticketID, projectSlug, currentPhaseSlug string, phases any, csrf string) map[string]any {
		return map[string]any{
			"TicketID":         ticketID,
			"ProjectSlug":      projectSlug,
			"CurrentPhaseSlug": currentPhaseSlug,
			"Phases":           phases,
			"CSRF":             csrf,
		}
	},
	// deref dereferences a *string for templates; nil → "".
	"deref": func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	},
	// derefCol dereferences a *domain.Column for templates; nil → "".
	"derefCol": func(c *domain.Column) string {
		if c == nil {
			return ""
		}
		return string(*c)
	},
	// list packages variadic args into a slice for template ranging — useful
	// for short ad-hoc enumerations like search-tab kinds where stating them
	// inline beats threading a Go-side constant through the page data.
	"list": func(items ...string) []string { return items },
	// hasSuffix is strings.HasSuffix exposed to templates. The sidebar uses
	// it to highlight the active per-project nav item by matching the
	// request URL against /board, /phases, /summary, etc.
	"hasSuffix": strings.HasSuffix,
	// percentOf returns part/total as a 0-100 integer (floor). Used for
	// status-bar segment widths; total==0 returns 0 to avoid div-by-zero.
	"percentOf": func(part, total int) int {
		if total <= 0 {
			return 0
		}
		return part * 100 / total
	},
	// phaseSlug returns the slug of the ticket's current phase, or "" if
	// it's phase-less or the phase is missing from the slice.
	"phaseSlug": func(t *domain.Ticket, phases []*domain.Phase) string {
		if t == nil || t.PhaseID == nil {
			return ""
		}
		for _, p := range phases {
			if p.ID == *t.PhaseID {
				return p.Slug
			}
		}
		return ""
	},
}

// renderInline is a test-friendly helper that runs the same path as Page but
// captures the output to a buffer and skips the chrome provider. Not used by
// the runtime.
func (r *Renderer) renderInline(name string, opts PageOpts) ([]byte, error) {
	t, err := r.loadPage(name)
	if err != nil {
		return nil, err
	}
	data := PageData{
		Title:       opts.Title,
		CurrentSlug: opts.CurrentSlug,
		Body:        opts.Body,
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "_layout.tmpl", data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
