## Goal

Phase CRUD plus the "assign ticket to a phase" affordance and a waves view, mirroring `create_phase`, `get_phase`, `get_phase_summary`, `list_phases`, `update_phase`, `delete_phase`, `list_waves`, `assign_ticket_to_phase`.

## Why

Phases are sub-projects for bigger bodies of work; waves group tickets within them. Without UI for these, humans can't organise work past a single flat backlog. The board view (ticket 5) needs phase context to render correctly when the user is viewing a phase-scoped board.

## Scope

### Routes (handlers/phases.go)

| Method | Path | Purpose | Service call |
|--------|------|---------|--------------|
| GET  | `/p/{slug}/phases`                  | List phases for a project | `svc.ListPhases` |
| GET  | `/p/{slug}/phases/new`              | Create phase form | — |
| POST | `/p/{slug}/phases`                  | Create phase | `svc.CreatePhase(slug, name, summary, description)` |
| GET  | `/p/{slug}/phases/{phase}`          | Phase detail (header + tabs to board/summary, ticket-counts per wave) | `svc.GetPhase` + `svc.ListWaves` |
| GET  | `/p/{slug}/phases/{phase}/edit`     | Edit phase form | `svc.GetPhase` |
| POST | `/p/{slug}/phases/{phase}`          | Update phase metadata | `svc.UpdatePhase` |
| POST | `/p/{slug}/phases/{phase}/delete`   | Delete phase (with confirm) | `svc.DeletePhase` |
| GET  | `/p/{slug}/phases/{phase}/summary`  | Render summary markdown (read + in-place edit, mirror project summary) | `svc.GetPhaseSummary`, `svc.UpdatePhase` |
| GET  | `/p/{slug}/waves`                   | Wave-grouped view across all tickets in the project | `svc.ListWaves` + `svc.ListTickets(wave=N)` per group |
| POST | `/tickets/{id}/assign-phase`        | Reassign a ticket to a different phase (or none) | `svc.AssignTicketToPhase(id, phase_id_or_slug?, comment)` |

### Templates (under `internal/web/templates/pages/phases/`)

- `index.tmpl` — table of phases with active/total ticket counts.
- `new.tmpl` / `edit.tmpl` — name + summary (≥200 chars) + description form.
- `detail.tmpl` — header, tabs (Board, Summary, Waves), ticket counts.
- `summary.tmpl` — markdown view + in-place editor (reuse the markdown helper from ticket 3).
- `waves.tmpl` — sections per wave (`Wave 1`, `Wave 2`, …, `Unassigned (wave 0)`); each section lists tickets with title + column badge.
- `partials/phase_row.tmpl`, `partials/wave_section.tmpl`.
- `partials/assign_phase_form.tmpl` — embedded on the ticket detail page (ticket 5 reserves the slot); a `<select>` of all phases (plus "no phase") with a required comment textarea ("comment is required for the audit trail").

## Hard rules to surface

- **Reassign requires a comment** — `AssignTicketToPhase` errors if comment is empty; surface inline 422.
- **Phase summary minimum 200 chars** — same as project summary.
- **Delete cascades** to phase tickets — confirmation must say so.

## Sidebar / navigation

- The project detail page's "Phases" tab (ticket 3) links to `/p/{slug}/phases`. After this ticket lands, that link becomes alive.
- Active phase highlighted in nav breadcrumb on phase-scoped pages.

## Key references

- `internal/svc/service.go:CreatePhase,GetPhase,ListPhases,UpdatePhase,DeletePhase,GetPhaseSummary,AssignTicketToPhase,ListWaves`.
- `internal/mcptools/tools.go` — read `assign_ticket_to_phase` handler for the comment-required pattern; mirror it.

## Gotchas

1. **Phase slug uniqueness is per-project, not global** — server enforces; surface inline.
2. **`ListWaves`** returns wave numbers with counts; wave 0 is "unassigned". Render the unassigned section last with a subtle grey treatment.
3. **AssignTicketToPhase target**: `phase_id_or_slug` empty means "no phase". Provide a `<option value="">No phase</option>` in the select.
4. **Tickets in phase X moving to phase Y** does NOT change wave membership; surface that in the help text so humans don't expect waves to follow.

## Verification

- Create a phase from the project page → it appears on `/p/{slug}/phases` with `0 / 0` counts.
- Create a ticket inside that phase (cross-deps with ticket 5; can verify after both land); counts go to `1 / 1`.
- Reassign the ticket to a different phase via the form; verify counts update on both.
- Reassign with empty comment → 422 inline.
- Visit `/p/{slug}/waves` → ticket appears in the right wave section.
- Edit phase summary in place; verify markdown re-renders.
- Delete an empty phase; confirm gone.
- MCP cross-check: `mcp__tickets_please__list_phases` matches UI state.
- `go test ./internal/web/handlers/...` — phase handler tests.

## Out of scope

- Drag-and-drop ticket reordering between phases (button-based for v1).
- Wave reassignment UI on the ticket form (ticket 5 owns the wave field).
- Phase search.

## Notes

- Parallelizable with tickets 3, 5, 7. Coordinate only on the layout/sidebar contract from ticket 2.
- The `/tickets/{id}/assign-phase` route is a sibling endpoint to ticket 5's other ticket mutations — coordinate with ticket 5 on the `?slug=` hint convention to skip the multi-mount walk.
