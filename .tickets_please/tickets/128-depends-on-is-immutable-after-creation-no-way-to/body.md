## Summary
`depends_on` (and `parallelizable_with`) can **only** be set at `create_ticket` time. There is no way to add, change, or remove a ticket's dependencies after it exists. `update_ticket` exposes only `body` / `title` / `wave`; `move_ticket` and `assign_ticket_to_phase` don't touch deps either.

## How it surfaced
Scaffolding a multi-ticket plan with an umbrella ticket + children C1–C5. The umbrella was created first and the children reference it. I then wanted to wire a **hard `depends_on`** from C1–C5 onto the scaffold/umbrella ticket so `ready_only` ordering would enforce "do the scaffold first."

I couldn't:
- `update_ticket` can't set `depends_on`.
- Recreating C1–C5 with the dep baked in would mint **new ticket ids**, breaking the umbrella's existing id references.

So the hard gate was unenforceable. I fell back to **wave ordering + "do scaffold first" banners** in the bodies — advisory only, not the hard `depends_on` gate the system otherwise supports.

## Impact
- `depends_on` is the documented *hard* gate (vs. waves which are advisory). Being unable to edit it after creation means any dependency you discover *after* a ticket exists is unenforceable without destructive recreate.
- Recreate-to-fix is destructive: new ids cascade-break any tickets that already reference the old id (umbrellas, parallelizable_with, comments citing the ticket).
- Common real-world flow — scaffold tickets, *then* realise the ordering — can't be expressed.

## Proposed fix
Let dependencies be mutable post-creation. Either:
1. Add `depends_on` / `parallelizable_with` to `update_ticket` (replace-set semantics), **or**
2. Dedicated `add_dependency` / `remove_dependency` tools taking `ticket_id` + `depends_on_id` (+ the mandatory column-move-style `comment` for the audit trail).

Guard against cycles on write (reject if the new edge would create a dependency cycle). Option 2 is friendlier for incremental edits and keeps an explicit audit comment per edge.
