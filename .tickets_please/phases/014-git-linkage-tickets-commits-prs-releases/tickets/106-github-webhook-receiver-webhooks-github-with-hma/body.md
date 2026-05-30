For remote tickets_please servers with GitHub-hosted code, accept signed webhook deliveries from GitHub for realtime indexing.

## Acceptance

- New route `POST /webhooks/github`, public (no session required) but enforces:
  - `X-Hub-Signature-256` header presence + valid HMAC-SHA256 against the per-project webhook secret (stored on project settings).
  - `X-GitHub-Event` header valid (we handle `push`, `pull_request`, `release`).
  - The `repository.full_name` payload field matches a known project's `git.remote`.
- On valid `push`: trigger `Indexer.Refresh` for the affected project (async).
- On valid `pull_request`: refresh PR cache; publish `PRChanged` event to the SSE hub (`project:{id}` and per-affected-ticket topics).
- On valid `release`: refresh release index; publish `ReleaseShipped` event to SSE.
- New project settings field: webhook URL display + secret rotate button + setup-guide link (e.g. "paste this URL + secret into github.com/owner/repo/settings/hooks").
- Tests cover: invalid signature rejected, valid signature accepted, unknown event-type ignored, repo-mismatch rejected.

## Hints

- Use `crypto/hmac` + `crypto/sha256` for verification; constant-time compare.
- The webhook secret is per-project, generated on first project setup (random 32 bytes hex).
