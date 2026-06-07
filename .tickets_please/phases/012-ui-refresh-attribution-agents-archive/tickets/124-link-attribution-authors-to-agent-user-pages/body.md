Attribution chips rendered the author as plain text even though `attribution.AgentLink` / `UserLink` already existed and the `.attr-link` style was in place. So there was effectively no linking to the agents page from ticket/comment references.

Fix: `Chip` and `CommentChip` now render the author via `AgentLink(CreatedBy.ID, name)` → `/agents/{id}`, falling back to `UserLink(CreatedFor)` → `/u/{id}`, then plain text. Mirrors `Label`'s fallback order so the displayed name is unchanged — just clickable. (Sidebar already had a top-level Agents link.)
