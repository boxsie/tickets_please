## Testing evidence
Ran `make build`, `go test ./internal/web`, and `git diff --check`. Confirmed generated `internal/web/static/app.css` contains the new typography stack and display font token.

## Work summary
Replaced the web UI's default system font stack with named display/body/mono stacks, increased base readability slightly, removed negative heading tracking, strengthened display treatment for headers/cards/metric values, and regenerated embedded CSS.

## Learnings
Typography for this UI is centralized in `internal/web/static/_src/app.css` under Tailwind v4 `@theme`; change source tokens first, then run `make build` so `internal/web/static/app.css` is regenerated and embedded. Avoid external font CDNs for the single-binary/local-first posture; named local-first stacks plus Noto/System fallbacks improve character without adding runtime assets.
