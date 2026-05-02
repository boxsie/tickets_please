---
id: T16
title: Phase methods + AssignTicketToPhase
status: TODO
owner: ""
depends_on: [T02, T03, T04, T05, T15]
parallelizable_with: [T07]
wave: 5
files:
  - internal/svc/phases.go
  - internal/svc/tickets.go
  - internal/store/phases.go
  - internal/store/tickets.go
estimate: medium
stretch: false
---

# T16 — PhaseService + AssignTicketToPhase

## Scope

Add **phases** as optional sub-projects: organizational containers, each with a required ≥200-char markdown summary, no own lifecycle. Tickets get an optional `phase_id`; `AssignTicketToPhase` moves them around with a mandatory comment.

**In:** `PhaseService` (Create/Get/List/Update/Delete), `AssignTicketToPhase` on `TicketService`, store helpers, file layout under `projects/<slug>/phases/<NNN>-<phase-slug>/`.

**Out:** No phase lifecycle (no `CompletePhase`, no states). Tickets keep their column/lifecycle independent. No nested phases.

## Files

- `internal/svc/phases.go` — phase methods on `Service`
- `internal/store/phases.go` — read/write/walk helpers
- Extensions to `internal/svc/tickets.go` (`AssignTicketToPhase`)
- Extensions to `internal/store/tickets.go` (move directory between `tickets/` and `phases/<NNN>-…/tickets/`)

## Details

### File layout

```
projects/<slug>/
├── tickets/                     # phase-less tickets
│   └── <NNN>-<ticket-slug>/
└── phases/
    └── <NNN>-<phase-slug>/
        ├── phase.yaml
        ├── summary.md
        ├── summary.embedding.json
        └── tickets/
            └── <NNN>-<ticket-slug>/
```

`<NNN>` on tickets is **project-global** — moving a ticket between phases doesn't renumber. Phase `<NNN>` is per-project.

### `phase.yaml` example

```yaml
id: 5b2c4d6e-…
project_id: 7e2f4a4d-…
slug: shipping-mvp
number: 2
name: Shipping the MVP
description: Make it live for our friends.
created_by: 8a51c2c0-…
created_at: 2026-05-02T15:00:00.000Z
updated_at: 2026-05-02T15:00:00.000Z
```

`summary.md` is the required ≥200 char markdown doc.

### RPCs

**`CreatePhase(project_id_or_slug, name, summary, description?)`**
- Validate `summary >= 200` chars after trim.
- Lazy-load project; take write lock.
- Compute next phase number = `len(loaded.Phases) + 1`.
- Build dir name `<NNN>-<slug.Make(name)>`.
- StageOp: `phases/<dir>/phase.yaml` + `phases/<dir>/summary.md`.
- Auto-commit: `[tickets_please] create phase <project>/<phase> [<agent>]`.
- Enqueue `JobProjectSummary` reusing the same kind for the resident summary index (or add `JobPhaseSummary` — pick one; reusing the project summary index is fine for v1, both kinds are searchable via `search_projects` + `search_phases`).

**`GetPhase(project_id_or_slug, phase_id_or_slug)`** — straightforward read.

**`ListPhases(project_id_or_slug)`** — returns all phases for the project plus computed `ticket_count` and `active_ticket_count`. No pagination v1.

**`UpdatePhase(id, name?, description?, summary?)`** — same pattern as UpdateProject. Summary changes trigger re-embed.

**`DeletePhase(id)`** — refuse if any tickets are still assigned to the phase (`FailedPrecondition` listing `<N>` tickets blocking). To delete, agents must reassign the tickets first.

**`AssignTicketToPhase(ticket_id, phase_id?, comment)`**
- `phase_id` empty → move to phase-less (under `projects/<slug>/tickets/`).
- Validate `comment` non-empty (non-trim).
- Take project write lock.
- Compute the source dir and target dir.
- Use `os.Rename` for the ticket's directory inside the StageOp (rename works if both paths share a filesystem, which they do since both are under `data_dir`). The full directory rename is atomic.
- Update `ticket.yaml.phase_id`.
- Insert a `system_move` comment (reuse `MoveTicket`'s comment pattern, but `to_column`/`from_column` are nil and the body is the agent-supplied comment with a prefix like `Phase reassignment: → <phase-name>`).
- Auto-commit: `[tickets_please] reassign ticket <project>/<NNN> to phase <phase> [<agent>]`.
- No re-embed needed (body content didn't change).

### Project cache integration

`LoadedProject` extends:

```go
type LoadedProject struct {
    Project       *domain.Project
    Phases        map[string]*domain.Phase     // id → phase
    PhasesBySlug  map[string]*domain.Phase
    Tickets       map[string]*domain.Ticket    // id → ticket (any phase or none)
    Comments      map[string][]*domain.Comment
    Vectors       *vecindex.Index
    LoadedAt      time.Time
    LastAccessAt  time.Time
    Lock          sync.RWMutex
}
```

Loader walks both `projects/<slug>/tickets/` and `projects/<slug>/phases/*/tickets/` to populate `Tickets` (with each ticket's `phase_id` set or null based on its location).

### MCP tools

(T12 implements these — descriptions canonical in [`../SPEC.md`](../SPEC.md).)

| Tool | Backing RPC |
|---|---|
| `create_phase` | `PhaseService.CreatePhase` |
| `list_phases` | `PhaseService.ListPhases` |
| `get_phase_summary` | `PhaseService.GetPhase` (returns just `summary`) |
| `update_phase` | `PhaseService.UpdatePhase` |
| `assign_ticket_to_phase` | `TicketService.AssignTicketToPhase` |

## Acceptance criteria

- [ ] `Service.CreatePhase` with summary < 200 chars → `domain.ErrInvalidArgument`.
- [ ] Successful `CreatePhase` writes `projects/<slug>/phases/<NNN>-<phase-slug>/{phase.yaml,summary.md}` with the expected number prefix.
- [ ] `ListPhases` returns phases ordered by number with correct ticket counts.
- [ ] `AssignTicketToPhase` with empty comment → `domain.ErrInvalidArgument`.
- [ ] `AssignTicketToPhase(ticket_id, phase_id=nil, comment=…)` moves a phased ticket back to project-level (`projects/<slug>/tickets/`); on-disk dir is renamed atomically; `ticket.yaml.phase_id = null`.
- [ ] `AssignTicketToPhase` produces a `system_move` comment with the supplied body and the agent recorded as author.
- [ ] `DeletePhase` with active tickets → `domain.ErrFailedPrecondition`; with all tickets reassigned → success.
- [ ] `LoadProject` with mixed phased + phase-less tickets returns the correct Tickets map.
- [ ] `ListTickets(project_id_or_slug=foo, phase_id_or_slug="bar")` returns only phase-bar tickets.
- [ ] `ListTickets(project_id_or_slug=foo, phase_id_or_slug="-")` returns only phase-less tickets (sentinel `"-"` documented).

## Notes

See **Phases (optional sub-projects)** in [`../SPEC.md`](../SPEC.md). Phases are intentionally minimal — no lifecycle, no required retrospective. The summary is the only content discipline; everything else is naked organization.

Coordinate with T12 (MCP) for the new tool registration.
