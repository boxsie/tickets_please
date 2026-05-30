Wire two OAuth providers behind a small provider interface so future providers (passkeys, Tailscale, magic-link) plug in cleanly.

## Acceptance

- `internal/auth/oauth.go` defines `Provider{Name, AuthorizeURL(state, redirectURL), Exchange(ctx, code, redirectURL) (claims, error)}`.
- `internal/auth/providers/github.go` + `internal/auth/providers/google.go` implement it (use `golang.org/x/oauth2`).
- Config (in `~/.tickets_please/config.yaml`):
  ```yaml
  auth:
    base_url: https://tickets.example.com   # for redirect URL construction
    providers:
      github: { client_id: ..., client_secret: ... }
      google: { client_id: ..., client_secret: ... }
  ```
- Routes:
  - `GET /auth/login` — login page with one button per configured provider.
  - `GET /auth/{provider}/start` — generates CSRF state, sets cookie, redirects to provider.
  - `GET /auth/{provider}/callback` — verifies state, exchanges code, upserts User via store, sets session cookie, redirects to original target.
  - `POST /auth/logout` — clears session cookie.
- Session cookie: signed (HMAC-SHA256 with a server secret), `HttpOnly`, `Secure` when `base_url` is HTTPS, `SameSite=Lax`. 30-day expiry, sliding.
- New session-cookie machinery COEXISTS with the existing MCP agent-session id for now — they get unified in [[auth-middleware-route-guards-csrf-reconciliation]].
- Tests cover: provider stub returns claims → user upserted → session cookie set → next request authenticated.

## Hints

- Use `crypto/rand` for state; store state+target in a short-lived cookie, not in server state.
- Read `auth.base_url` from config; if empty, infer from `r.Host` (dev mode).
