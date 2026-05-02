# planning/ — tickets for `tickets_please`

This directory is the work queue. Each `T0X-*.md` file is a self-contained ticket an agent can claim and execute. The full design spec lives at [`../SPEC.md`](../SPEC.md) — every ticket points back to specific sections of it.

## Subagent-friendly

Every ticket file starts with a YAML frontmatter block so an orchestrator agent can parse the dependency graph without scraping markdown. The schema:

```yaml
---
id: T04                                  # ticket ID, matches filename prefix
title: ProjectService + project cache    # short title
status: TODO                             # TODO | IN_PROGRESS | DONE | BLOCKED
owner: ""                                # agent name when claimed; empty otherwise
depends_on: [T02, T03, T15]              # ids that must be DONE before starting
parallelizable_with: [T05, T06, T08]     # advisory; safe to run in parallel
wave: 2                                  # 0=foundation; later waves depend on earlier
files:
  - internal/svc/service.go
  - internal/svc/projects.go
estimate: medium                         # tiny | small | medium | large
stretch: false                           # true = nice-to-have, not required for v1
---
```

This is the same shape as the runtime `Ticket` type (see **Ticket dependencies & subagent orchestration** in [`../SPEC.md`](../SPEC.md)) — the planning queue dogfoods the system it's specifying.

## How to claim a ticket

1. Walk this directory; pick a ticket whose `depends_on` list is fully `DONE` and whose `status` is `TODO`.
2. Set `status: IN_PROGRESS` and `owner: <your-handle>` in the frontmatter.
3. Do the work. Tick the acceptance boxes as you go.
4. When all acceptance criteria pass, set `status: DONE` and move on.

One agent per ticket. If multiple agents need to coordinate edits to the same file (e.g. T04/T05/T06 all touch `cmd/tickets_please/main.go`), the second agent rebases on the first's changes — the dependency waves below are designed so this rarely happens.

## Status legend

| Value | Meaning |
|---|---|
| `TODO` | Up for grabs. |
| `IN_PROGRESS` | Owner is actively working on it. |
| `DONE` | Acceptance criteria met. Safe to depend on. |
| `BLOCKED` | Hit a wall; needs human input. Drop a note in the ticket body. |

## Dependency graph

```
T01 ─┬─ T02 (storage prims) ─┬─ T15 (Service skeleton + agents) ─┬─ T04 (Projects + cache) ─┬─ T05 (Tickets CRUD) ─┬─ T07 (Move/Complete) ──┐
     │                       │                                   │                          │                      │                        │
     ├─ T03 (domain types) ──┘                                   │                          ├─ T06 (Comments) ─────┘                        │
     │                                                            │                          │                                              │
     ├─ T08 (embed providers) ───────────────────────────────────┐                          └─ T16 (Phases) ────────────────────────────────┤
     │                                                           │                                                                          │
     └─ T02 ── T09 (vec index) ─────────────────────────────────┴────────────── T10 (worker) ───── T11 (search) ───── T12 (MCP binary) ────┘

  Stretch (Wave 9): T13 (tests), T14 (polish)
```

## Suggested execution waves

The wave number is the **earliest** wave a ticket can start in (i.e. all its `depends_on` are satisfied by the previous wave's completions).

- **Wave 0** — T01 alone. Sequential.
- **Wave 1** — T02, T03, T08 in parallel.
- **Wave 2** — T15 (needs T02+T03), T09 (needs T02). Parallel.
- **Wave 3** — T04 (needs T02+T03+T15). Solo wave — T05/T06 depend on T04 so they wait.
- **Wave 4** — T05, T06 (each needs T02+T03+T04+T15). Parallel.
- **Wave 5** — T07 (needs T05+T06), T16 (needs T04+T05+T15). Parallel.
- **Wave 6** — T10 (needs T07+T08+T09).
- **Wave 7** — T11 (needs T10).
- **Wave 8** — T12 (needs T03+T04+T05+T06+T07+T11+T15+T16).
- **Wave 9 (stretch)** — T13, T14 in parallel.

A subagent orchestrator should pick from "any TODO ticket whose deps are all DONE", not strictly walk waves — waves are just a hint. The frontmatter `wave:` value tells the orchestrator the earliest possible starting wave.

## Ticket index

| ID | Title | Status | Wave | Depends on |
|---|---|---|---|---|
| [T01](T01-bootstrap.md) | Bootstrap module, Makefile, single binary, data dir | DONE | 0 | — |
| [T02](T02-storage.md) | Storage primitives, locks, fsnotify, records, integrity | TODO | 1 | T01 |
| [T03](T03-domain.md) | Domain types & MCP tool schemas | DONE | 1 | T01 |
| [T08](T08-embed-providers.md) | Embedding providers (Ollama, OpenAI) | TODO | 1 | T01 |
| [T09](T09-vec-index.md) | Vector index (in-memory) | TODO | 2 | T02 |
| [T15](T15-agent-identity.md) | Service skeleton + agent identity + middleware | TODO | 2 | T02, T03 |
| [T04](T04-projects.md) | Project methods + project cache | TODO | 3 | T02, T03, T15 |
| [T05](T05-tickets.md) | Ticket CRUD methods | TODO | 4 | T02, T03, T04, T15 |
| [T06](T06-comments.md) | Comment methods | TODO | 4 | T02, T03, T04, T15 |
| [T07](T07-move-complete.md) | MoveTicket + CompleteTicket | TODO | 5 | T05, T06 |
| [T16](T16-phases.md) | Phase methods + AssignTicketToPhase | TODO | 5 | T02, T03, T04, T05, T15 |
| [T10](T10-embed-worker.md) | Embedding worker (JSON sidecars) | TODO | 6 | T07, T08, T09 |
| [T11](T11-search.md) | Search methods (semantic) | TODO | 7 | T10 |
| [T12](T12-mcp-binary.md) | MCP binary entry point | TODO | 8 | T03, T04, T05, T06, T07, T11, T15, T16 |
| [T13](T13-integration-tests.md) | Integration tests (stretch) | TODO | 9 | T07, T11, T15 |
| [T14](T14-polish.md) | Polish (stretch) | TODO | 9 | T07, T12 |

## Where the spec lives

[`../SPEC.md`](../SPEC.md) is the source of truth for:
- **Context** — why we're building this and the load-bearing rules.
- **Tech stack** — every library, with reasons.
- **Design decisions** — Ollama default, immutable comments, no reopen, agents-attribute-everything.
- **Configuration** — config file, env vars, every key.
- **Agent identity & sessions** — register / heartbeat / in-process middleware.
- **Project summary** — required markdown doc per project.
- **Project layout** — directory structure on disk.
- **Data layout** — `.tickets_please/` filesystem schema.
- **Project loading & in-memory cache** — sliding TTL, lazy + optional model.
- **Ticket dependencies & subagent orchestration** — `depends_on` / `parallelizable_with` / `ready_only`.
- **Service API** — every method on `svc.Service` and key input/output shapes.
- **Validation & enforcement** — exact write sequences for Move/Complete.
- **Embedding pipeline** — when/how/where embeddings happen.
- **Vector search index** — in-memory cosine index.
- **MCP server** — full tool table with the LLM-facing descriptions.

If a ticket leaves something ambiguous, the spec is the tiebreaker. If both leave it ambiguous, set `status: BLOCKED` and add a question.
