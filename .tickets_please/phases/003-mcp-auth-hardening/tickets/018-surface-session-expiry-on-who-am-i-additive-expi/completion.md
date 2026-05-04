## Testing evidence
Added two new tests in `internal/mcptools/register_agent_test.go`:

1. `TestWhoAmI_ExpiredField` — registers an agent and asserts the `who_am_i` payload contains `"expired": false`. Then mutates the cached `Session.ExpiresAt` to `now-1m` (the registry returns a pointer, so the in-place mutation is the right knob — `who_am_i` reads from the registry, not the AgentStore). Re-invokes `who_am_i` and asserts `"expired": true` while `"registered"` stays `true` (semantic = entry exists, not authenticated).
2. `TestWhoAmI_UnregisteredOmitsExpired` — without calling `register_agent`, invokes `who_am_i` and asserts `registered=false` and that the `expired` key is absent from the response (no expiry to report).

Verbose run (filter `WhoAmI`):
```
=== RUN   TestRegisterAgent_WhoAmIReflectsMetadata
--- PASS: TestRegisterAgent_WhoAmIReflectsMetadata (0.00s)
=== RUN   TestWhoAmI_ExpiredField
--- PASS: TestWhoAmI_ExpiredField (0.00s)
=== RUN   TestWhoAmI_UnregisteredOmitsExpired
--- PASS: TestWhoAmI_UnregisteredOmitsExpired (0.00s)
=== RUN   TestWhoAmI
--- PASS: TestWhoAmI (0.00s)
```

Full `go test ./internal/mcptools/ -count=1 -v` is green (all 23+ tests). `go vet ./...` and `gofmt -l internal/mcptools/` clean.

There is a pre-existing failure in `internal/web/TestLoadProject_Stub` flagging POST → 403/404 instead of 501/405 — that test is in `internal/web/web_test.go` (a directory entirely untracked in git, part of in-progress phase 002-web-frontend ticket 011 work). The test comment itself says "POST 501s (ticket 3 wires it)". My changes touch only `internal/mcptools/` and there is no import path between web and mcptools (`grep -l "mcptools" internal/web/*.go` returns nothing); the failure is unrelated to this ticket.

## Work summary
**internal/mcptools/tools.go** — added one entry to the `out` map in `handleWhoAmI` (the registered branch): `"expired": !sess.ExpiresAt.IsZero() && time.Now().After(sess.ExpiresAt)`. The value is computed on read so it never lies as time passes; `IsZero()` guards the bootstrap stdio Session created by `DefaultStdioSession` ([internal/mcptools/identity.go:99-119](internal/mcptools/identity.go#L99-L119)) — that path registers a Session before `svc.RegisterAgent` populates `ExpiresAt`, and a pre-bootstrap session should report `expired: false`. Comment in the source spells out the rationale.

The unregistered branch of `handleWhoAmI` is untouched: the response still has `registered: false` plus the existing nil fields and **no** `expired` key — there is no expiry to report when there is no Session.

**internal/mcptools/register_agent_test.go** — added `TestWhoAmI_ExpiredField` (toggles between fresh and force-expired Session, asserts both states) and `TestWhoAmI_UnregisteredOmitsExpired` (asserts the unregistered shape is unchanged, no `expired` key).

No other files touched. Pairs with T001 in this phase: T001 makes the auth state self-healing on mutations, T002 makes it visible to clients without parsing timestamps.

## Learnings
**The cached Session is the right knob for `who_am_i` test mutations, not the on-disk AgentStore.** `handleWhoAmI` reads exclusively from the in-memory Registry — it never calls `s.AgentStore.ReadAgent`. So forcing `AgentRecord.ExpiresAt` on disk (the trick the T001 auto-refresh tests use) wouldn't change `who_am_i`'s output. The correct test setup is `tools.registry.Get("stdio").ExpiresAt = time.Now().Add(-time.Minute)` — `Registry.Get` returns the live pointer, and in-place mutation propagates because the registry stores `*Session`.

**Reading two independent flags (`registered`, `expired`) is more honest than overloading one.** I considered making `registered` mean "registered AND active" (i.e., flip to false on expiry), but that's a behavior change for any existing client and conflates two questions: "is there a Session" vs "is that Session usable". Keeping `registered` as Registry-presence and adding `expired` as a separate boolean lets clients ask either question. `who_am_i` is introspection, not enforcement — clarity beats cleverness.

**`IsZero()` guard catches the pre-svc-bootstrap stdio Session.** `DefaultStdioSession` builds a Session with `AgentKey`/`AgentName` set but no `AgentID` and no `ExpiresAt`. If the stdio path ever registers that Session before calling `svc.RegisterAgent`, `who_am_i` would otherwise compute `time.Now().After(time.Time{})` → `true` and lie. The guard handles that edge cleanly: `!sess.ExpiresAt.IsZero() && time.Now().After(sess.ExpiresAt)` is `false` for a zero `ExpiresAt`. Worth a comment in the source so a future reader doesn't golf it away.

**Asserting field absence requires `_, present := got["expired"]`** rather than `got["expired"] == nil`, because Go's JSON unmarshal into `map[string]any` only puts present keys in the map; an absent key is **not** the same as a `nil` value. Both `TestWhoAmI_UnregisteredOmitsExpired` and any future "field-absent" test should follow this pattern.

**`json.Unmarshal` into a `map[string]any` decodes JSON `false` as Go `bool(false)`, not `interface{}(nil)`.** So `got["expired"] != false` is the right comparison — `got["expired"] != nil` would pass even when the field reads `false`. Caught this by writing the test once with the wrong comparator and watching it pass even when I removed the `expired` field; the second assertion form (`!= false`) is what actually pins the value.
