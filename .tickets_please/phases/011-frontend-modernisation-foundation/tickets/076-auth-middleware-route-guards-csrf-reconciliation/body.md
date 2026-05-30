Enforce auth on the web surface and unify CSRF + session handling. The web UI requires login; the MCP surface keeps its own agent-key auth (untouched).

## Acceptance

- `authMiddleware` reads the session cookie, hydrates `User` from store, attaches to request context (`auth.UserFrom(ctx)`).
- Route policy:
  - Public: `/auth/*`, `/healthz`, `/static/*`, `/sse` (per-stream auth check inside).
  - Authenticated: every other web route. Redirect to `/auth/login?next=<original>` on miss.
  - `/mcp` is NOT touched — agent-key auth as today.
- Per-project guards: `requireMember(role)` middleware on `/p/{slug}/...` reads membership for the user+project, 403s if missing or insufficient role.
  - `viewer`: GET only.
  - `member`: GET + ticket/comment mutations.
  - `owner`: everything including project settings/delete.
- CSRF: the existing per-session token is reused, but the session id now comes from the user cookie, not the legacy `svc.SessionIDFrom`. Old `svc.SessionIDFrom` either bridged to the new context or replaced.
- Tests cover: unauth → redirect; auth no-membership → 403; viewer attempting POST → 403; member POST OK; owner DELETE OK.

## Hints

- Tap into `internal/web/router.go`'s existing `wrap` helper — replace it with an auth-aware wrapper.
- For `/sse`, validate the cookie on the initial GET; SSE has no per-event auth so the connection is the trust boundary.
