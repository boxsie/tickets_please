When a ticket is completed, snapshot the commits that implemented it into the immutable completion record. The git-index is a cache that can be lost; completion is sacred and must include the shipping record.

## Acceptance

- `complete_ticket` flow gains a step: query `CommitsForTicket` at completion time; serialise the result into the `system_completion` comment body (or a structured field on the completion record).
- Format (in the comment, after the existing testing_evidence/work_summary/learnings sections):
  ```markdown
  ### Implementation commits

  - `abc1234` · feat(search): add feedback loop · Claude · +180/-12
  - `def5678` · test(search): cover edge cases · Claude · +24/-3
  ```
- If the index has no commits for the ticket at completion time (the agent didn't push yet, or refs missing), the section is omitted; a logged WARN points at the missing linkage.
- Existing completed tickets are NOT backfilled (completion is frozen) — the snapshot only applies to new completions after this lands.
- Tests cover: completion with and without indexed commits; section format; idempotency on retry.

## Hints

- This is the only place where commit data becomes part of the "sacred" trail — everywhere else it's derivable from the cache.
- Don't fail completion if the index lookup fails — log + proceed with section omitted.
