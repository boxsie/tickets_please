Promote the W1 scaffold into a production hub. Per-user, per-topic, with reconnect.

## Acceptance

- `Hub.Subscribe(user, topics []string) (chan Event, unsub func())` — events fan out only to subscribers of matching topics.
- Topic shapes: `project:{id}`, `ticket:{id}`, `phase:{id}`, `agent:{id}`, `global:agents`. Subscribers join multiple.
- Per-event monotonic `Event.ID` (uint64). Hub keeps a ring buffer of the last N=1024 events per topic.
- On SSE connect with `Last-Event-ID` header, replay buffered events newer than that id, then attach for live.
- Authorization: `/sse?topics=project:abc,ticket:xyz` — hub validates the user has membership on every requested topic via the auth store; rejects subscription to topics outside their scope.
- Heartbeat retained from W1 scaffold (`: heartbeat` every 25s).
- Tests cover: subscribe → publish → receive; reconnect with last-event-id replays only newer events; cross-tenant topic rejected; backpressure (slow consumer disconnected after N-buffered).

## Hints

- One `chan Event` per subscription with a small buffer (e.g. 64); on full, close + disconnect — clients reconnect.
- `Event.ID` allocated by hub on publish, not by caller.
