package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
)

// csrfPurpose is mixed into the HMAC so the CSRF token can't be confused
// with the cookie signature (different purpose, same secret root).
const csrfPurpose = "tp-csrf-v1"

// hmacSig returns hmac-sha256(purpose|msg) under the given secret. Every signed
// value in the web layer (cookie, CSRF token, OAuth state) goes through this so
// a single purpose-prefixed construction is used everywhere — distinct purpose
// labels keep otherwise-identical messages from cross-validating.
func hmacSig(secret []byte, purpose, msg string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(purpose + "|" + msg))
	return mac.Sum(nil)
}

// csrfToken returns the per-session CSRF token: base64(hmac(agentID, key)).
// Stable for the lifetime of the cookie so refreshing a form page produces
// the same hidden field value.
func csrfToken(secret []byte, agentID string) string {
	return base64.RawURLEncoding.EncodeToString(hmacSig(secret, csrfPurpose, agentID))
}

// checkCSRF verifies the request's `_csrf` form field against the expected
// token for the supplied agentID. Returns nil on match. Constant-time compare
// to avoid timing leaks.
func checkCSRF(r *http.Request, secret []byte, agentID string) error {
	if err := r.ParseForm(); err != nil {
		return errCSRF("read form: " + err.Error())
	}
	got := r.Form.Get("_csrf")
	want := csrfToken(secret, agentID)
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return errCSRF("csrf token mismatch")
	}
	return nil
}

type csrfErr struct{ msg string }

func (e *csrfErr) Error() string { return e.msg }

func errCSRF(msg string) error { return &csrfErr{msg: msg} }
