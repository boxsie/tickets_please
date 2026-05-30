Pull in Datastar (https://data-star.dev/) and stand up the SSE plumbing. No actual events are published yet — that's W3. This ticket just lands the wiring so W3 can flip the switch.

## Acceptance

- Datastar JS embedded in `internal/web/static/` (download the released `datastar.js`, commit it, do not CDN-link).
- Base layout includes the script tag.
- `/sse` route exists, opens an `text/event-stream` response, holds the connection, and writes a heartbeat (`: heartbeat\n\n`) every 25s.
- A dev-only `/_dev/sse-ping` button on `/_dev/components` calls a server endpoint that pushes one test `&lt;span&gt;` patch to the SSE stream and Datastar swaps it into the page.
- `internal/web/sse/hub.go` defines a `Hub` interface with `Subscribe(topic) chan Event` / `Publish(topic, Event)` — concrete impl in-process (`memhub`) for now; tests cover subscribe → publish → receive.
- No identity or topic-scoping yet — that lands in [[sse-hub-per-session-topic-scoped]].

## Hints

- Datastar SSE protocol is plain SSE events with structured `event:` types (`datastar-patch-elements`, `datastar-patch-signals`). Lean on `r/datastar` examples.
- Use `http.Flusher` after each write; document the proxy implications (buffering) in code comment.
