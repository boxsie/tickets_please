## Symptom (reported by user)

> "I don't feel like they are using feedback or learnings enough."

The two load-bearing feedback loops in tickets_please are under-exercised by agents in practice:

- **`search_learnings`** before starting work (the "most common avoidable mistake" per the server instructions is skipping it) — agents often dive in without it.
- **`rate_search_result`** after searches — agents rarely rate hits, so the ranking signal that's supposed to sink stale learnings and float good ones never accumulates.

This is the core dogfooding promise ("the system feeds itself") quietly not happening.

## Why it matters

`learnings` is the only completion field that earns its keep across tickets (ticket `bda1ec08`) — and it's only valuable if (a) agents *search* it before working and (b) agents *rate* results so ranking improves. Without the rate loop, `search_learnings`/`search_tickets` ranking is static and rot doesn't sink.

## Possible levers (investigate, pick what's effective)

1. **Server `instructions` / tool descriptions** — make the reflexes louder and more imperative. The `search_*` tool responses already return a `feedback_hint` with `entry_keys` + a nudge; consider making that hint harder to ignore, or having every search response lead with "rate these with rate_search_result".
2. **Completion-time prompt** — `complete_ticket`'s description could remind the agent to rate the learnings/tickets that actually helped during the work.
3. **Friction reduction** — is `rate_search_result` easy enough to call in one shot from a search response? The `feedback_hint.entry_keys` batch pattern is good; make sure descriptions point at it clearly.
4. **Measurement** — can we cheaply tell how often searches happen vs. how often ratings follow? Even a rough log/metric would tell us whether interventions move the needle.

## Out of scope (for now)

- Hard-gating work behind a mandatory `search_learnings` call (too heavy-handed; nudges first).

## Acceptance

- A concrete, shipped change (instruction/description wording and/or response-shape nudge) that measurably increases the odds an agent searches learnings before working and rates results after.
- Note in the ticket which lever(s) were chosen and the reasoning, so we can iterate if the behaviour doesn't shift.

## Note

This is a softer, behavioural ticket than the other three in this batch — the "fix" is prompt/UX engineering of the agent-facing surface, not a hard bug. Worth a short spike to decide the highest-leverage intervention before building.
