## Testing evidence
make check green (exit 0): build, vet, full go test, templ generate, tailwind regen, windows cross-compile. New tests in internal/web/auth_test.go, internal/auth/providers/github_test.go, internal/config/auth_config_test.go. Full detail in the follow-up comment.

## Work summary
GitHub + Google OAuth login flow for the web UI; committed to main as 5c841ce (tickets-please/075). Provider interface + github/google impls, nested auth config, UserStore wired into svc.Service, /auth/* routes, signed tp_user session cookie. Full detail in the follow-up comment.

## Learnings
OAuth login flow (ticket 075). Key gotchas captured in detail in the follow-up comment; headline: hardcode oauth2.Endpoint literals to avoid the x/oauth2/google cloud-metadata transitive, and trust make check over gopls bind-mount false positives.
