## Symptom (reported by user)

> "The sign-in window is too short, we should bump it to indefinite tbh — or maybe like a day actually."

The web UI login session times out far too aggressively, forcing re-auth during normal use. This is a single-user hobby instance behind the homelab; there's no security reason to keep the window tight.

## Scope

This is the **web UI auth session** (cookie/login), *not* the MCP agent session TTL. Don't confuse the two:

- MCP agent sessions already auto-refresh on expiry (ticket `9b06036c` — `callWithRetry` auto-refresh). Leave that alone.
- This ticket is the human-facing login cookie lifetime in the web frontend.

Relevant files (recently touched in the current working tree): `internal/web/auth.go`, `internal/web/bootstrap.go`. Find where the session/cookie expiry (TTL / `MaxAge` / `ExpiresAt`) is set and bump it.

## Decision

User's stated preference, in order: **indefinite**, falling back to **~24h** if indefinite is awkward. Recommendation: make it a config value (default 24h or 7d) so it's tunable, with the option of 0 / no-expiry meaning "indefinite". A long-lived cookie is fine for this single-user homelab deployment — lean toward convenience over enterprise session hygiene.

## Acceptance

- After signing into the web UI, the session stays valid for at least a day (or indefinitely) without re-auth.
- Value is set in one obvious place (ideally config-driven, sensible default).
- No regression to the MCP agent-session path.
