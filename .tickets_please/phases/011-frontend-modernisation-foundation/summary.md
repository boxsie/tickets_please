## Phase: Frontend modernisation foundation

The current web UI is `html/template` + htmx + hand-rolled CSS — it works, but as the surface grows it feels like a 90s server-rendered admin tool. Before piling more features on, swap the bones to something modern, then build everything else on top. We're staying server-rendered (single binary is sacred, see SPEC.md), but moving to typed components, modern styling, declarative reactivity, realtime push, and real multi-user auth.

## Decided stack

- **templ** for typed Go components (kills `html/template` footguns, gives IDE autocomplete, composable).
- **Tailwind v4** for utility-first styling, embedded via a `make` step (no Node runtime).
- **Datastar** for declarative hypermedia reactivity. Plays great with templ + SSE; small mental model.
- **SSE** for realtime push (column changes, comments, agent activity). One-way is enough for our model.
- **Multi-user auth**: cookie sessions + GitHub OAuth (primary) + Google OAuth (secondary). User/Membership models. Per-project roles (owner / member / viewer). Built so this can be exposed to the internet eventually.
- **Agent identity bridge**: agents stay key-authenticated for MCP; on user-owned projects, agent attribution shows as "Claude (acting for Dan)".

## Hard rules

- Single binary stays. No Node at runtime; Node only runs during `make`.
- Existing on-disk format (yaml + markdown + embedding sidecars) is untouched.
- MCP surface and behaviour are untouched — this is a pure UI/auth rewrite.
- Pages migrate 1:1 in W1 (no feature changes during the stack swap). Behaviour churn happens in Phase 2 on the new bones.
- CSRF + session handling get reconciled with the new cookie scheme, not bolted on.

## Waves

```
Wave 1 — Stack (templ + Tailwind + Datastar + SSE + page-by-page migration)
Wave 2 — Auth (User + Membership models, OAuth, guards, invitations, agent bridge)
Wave 3 — Realtime (SSE hub + event bus + live patches + optimistic UI)
```

## Out of scope (deferred to Phase 2)

- New feature surfaces (agents page, archive UI, search ratings, attribution polish). All built on this foundation in the next phase.
