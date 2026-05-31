package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"crypto/rand"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
)

const (
	cookieName    = "tp_sid"
	cookiePurpose = "tp-cookie-v1"
	cookieMaxAge  = 7 * 24 * 60 * 60 // 7 days, matches RegisterAgent TTL
	agentTTL      = 7 * 24 * time.Hour
)

// sessionManager holds the stable HMAC secret for cookie signing + CSRF
// tokens. The secret is derived from cfg.DataRoot so cookies survive process
// restarts but can't be forged across different tickets_please installs.
//
// All session state is on disk (AgentStore yamls); this struct holds no
// in-memory map, so two requests racing on a fresh browser will each mint
// their own agent — harmless, the loser orphans a record that expires in 7d.
type sessionManager struct {
	deps   Deps
	secret []byte
}

func newSessionManager(deps Deps) *sessionManager {
	// Derive a stable secret from the data root. Not a real auth key — just
	// a tamper guard for a localhost UI. SHA-256 over a fixed-purpose label
	// + the data root path means two installs with different data roots
	// won't accidentally accept each other's cookies.
	sum := sha256.Sum256([]byte(cookiePurpose + "|" + deps.Cfg.DataRoot))
	return &sessionManager{deps: deps, secret: sum[:]}
}

// middleware injects an authenticated agent session into every downstream
// request's context. On miss/expiry it transparently mints a new agent and
// rewrites the cookie. Subsequent svc.Service calls find the session via
// svc.SessionIDFrom(ctx).
func (m *sessionManager) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentID, err := m.resolve(w, r)
		if err != nil {
			http.Error(w, "session: "+err.Error(), http.StatusInternalServerError)
			return
		}
		ctx := svc.WithSessionID(r.Context(), agentID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolve returns the agentID for the current request — reading the cookie
// when valid, minting a fresh agent (and rewriting the cookie) otherwise.
func (m *sessionManager) resolve(w http.ResponseWriter, r *http.Request) (string, error) {
	if id := m.readCookie(r); id != "" {
		// Verify the agent still exists and isn't expired. AgentStore is
		// authoritative — if the on-disk record is gone, the cookie is stale.
		if rec, err := m.deps.Service.AgentStore.ReadAgent(id); err == nil {
			if rec.ExpiresAt.After(time.Now()) {
				return id, nil
			}
		} else if !errors.Is(err, domain.ErrNotFound) {
			return "", err
		}
	}
	return m.mintAgent(w, r)
}

// mintAgent calls svc.RegisterAgent with a fresh random key, sets the
// signed cookie, and returns the new agentID.
func (m *sessionManager) mintAgent(w http.ResponseWriter, r *http.Request) (string, error) {
	suffix, err := randomHex(8)
	if err != nil {
		return "", fmt.Errorf("mint agent: random: %w", err)
	}
	key := "web-ui:" + suffix
	meta := map[string]string{
		"client_name": "Web UI",
		"client_kind": "browser",
		"ua":          truncate(r.UserAgent(), 200),
		"remote_addr": r.RemoteAddr,
	}
	id, _, err := m.deps.Service.RegisterAgent(r.Context(), key, "Web UI", meta, agentTTL)
	if err != nil {
		return "", fmt.Errorf("mint agent: register: %w", err)
	}
	m.writeCookie(w, r, id)
	return id, nil
}

// readCookie returns the agentID from a valid signed cookie, or "" if the
// cookie is missing, malformed, or signature-invalid.
func (m *sessionManager) readCookie(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return ""
	}
	id := parts[0]
	gotSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	wantSig := m.sign(id)
	if subtle.ConstantTimeCompare(gotSig, wantSig) != 1 {
		return ""
	}
	return id
}

// writeCookie sets the tp_sid cookie carrying the agentID + HMAC signature.
// Secure attribute is set only when the request arrived over TLS so http
// localhost dev keeps working.
func (m *sessionManager) writeCookie(w http.ResponseWriter, r *http.Request, agentID string) {
	value := agentID + "." + base64.RawURLEncoding.EncodeToString(m.sign(agentID))
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

// sign returns hmac(agentID) for cookie signature verification.
func (m *sessionManager) sign(agentID string) []byte {
	return hmacSig(m.secret, cookiePurpose, agentID)
}

// agentLabel returns a short human-readable label for the agent ID, suitable
// for a top-bar identity badge. "Web UI · 3a9f1c". Empty if the context has
// no session.
func (m *sessionManager) agentLabel(ctx context.Context) string {
	id, ok := svc.SessionIDFrom(ctx)
	if !ok {
		return ""
	}
	suffix := id
	if len(suffix) > 6 {
		suffix = suffix[:6]
	}
	return "Web UI · " + suffix
}

// randomHex returns 2*n hex characters drawn from crypto/rand.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// truncate caps a string at n bytes (best-effort, doesn't respect rune
// boundaries — fine for header values like User-Agent).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
