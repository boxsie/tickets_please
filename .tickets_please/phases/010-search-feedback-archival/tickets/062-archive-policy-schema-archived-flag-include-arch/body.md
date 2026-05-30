## Goal

Introduce an archived state that's separate from the column workflow, expose it through the search surface as a default-excluded filter, and define the policy that drives auto-archival (the actual sweep lands in T6).

## Scope

### Domain change

Add `Archived bool` and `ArchivedAt *time.Time` to `domain.Ticket`. Serialised on `ticket.yaml`:

```yaml
archived: true
archived_at: 2026-04-01T10:00:00Z
```

Archived is **independent** of column. A `done` ticket can be archived (most common case); an `in_progress` ticket could be archived if the work is paused indefinitely (rare but legal). Hard rule: `done` tickets stay frozen for completion-field edits, but the `archived` flag can flip — flipping archived is its own audited action, not a freeze violation.

### Policy schema

Add `ArchiveConfig` to `domain.Project`, serialised on `project.yaml`:

```yaml
archive:
  enabled: false               # opt-in per project; default off
  min_age_days: 180            # only consider entries this old or older
  min_retrievals: 3            # ...AND retrieved at least this many times (means LLMs have seen it and ignored it / not rated it well)
  dislike_ratio: 0.5           # dislikes / (likes + dislikes) ≥ this → early-archive eligible
  early_archive_age_days: 30   # ...but not earlier than this
  auto_sweep_on_mount: false   # T6 wires this; here it's just the schema
```

Decision matrix (computed per entry — used by T6):

```
NEVER archive if archived already.
ARCHIVE if age ≥ min_age_days AND retrievals ≥ min_retrievals AND
        (dislike_ratio_signal OR no recent feedback).
EARLY ARCHIVE if age ≥ early_archive_age_days AND
              total_ratings ≥ 3 AND
              dislike_ratio ≥ configured threshold.
```

Document the matrix in a single helper `archive.Decide(t Ticket, fb FeedbackRecord, cfg ArchiveConfig, now time.Time) ArchiveDecision`. T6 calls this; T5 just defines + unit-tests the helper.

### Search integration

Every search method gets a new optional parameter:

```
search_tickets({query, project_id_or_slug?, top_k?, include_archived?: false})
search_learnings({..., include_archived?: false})
search_comments({..., include_archived?: false})
```

Default `false`. When `false`, the post-fetch filter drops any hit whose underlying ticket is archived (for learnings/comments: the parent ticket's archive flag rules).

Implementation: vec-index entries stay in place — exclusion is a post-filter on the candidate list. This makes unarchive free (no re-embed needed).

### List/get behaviour

- `list_tickets` gets a `include_archived` param too, default false.
- `get_ticket` returns archived tickets unconditionally (direct lookup by id is always allowed; you can't archive your way out of a direct reference).

### Manual archive/unarchive tools

```
archive_ticket({ticket_id, comment})
unarchive_ticket({ticket_id, comment})
```

Comment required, audited like a column move. The comment writes a `system_archive` / `system_unarchive` `CommentKind` (extend the enum). T6 wires the automatic version of `archive_ticket`.

Bump `totalTools` and `expectedTools` for the two new tools (and counting `apply_archive_policy` from T6, the tool count becomes 32 + 1(T2) + 3(T5/T6) = 36 — verify in T6 when totalling).

### Tests

- `archive.Decide` truth table covering each branch of the matrix.
- An archived ticket is excluded from `search_*` by default but appears when `include_archived: true`.
- `get_ticket` on an archived ticket succeeds.
- `list_tickets` without `include_archived` excludes them; with it, includes.
- `archive_ticket` writes an immutable `system_archive` comment; second call (already-archived) is a no-op with an explanatory error.
- `unarchive_ticket` round-trips.
- Project with `archive.enabled: false` ignores the policy in `archive.Decide` (returns `Decide.Skip`).

## Out of scope

- The sweep itself + auto-sweep-on-mount — T6.
- Archive semantics for completion learnings independent of their parent ticket — they always follow the parent (a learning isn't separately archivable).
- A web-UI archived view — defer; CLI/MCP first.

## Critical files

- `internal/domain/ticket.go` — `Archived`, `ArchivedAt`
- `internal/domain/project.go` — `ArchiveConfig`
- `internal/domain/archive/decide.go` (new) — pure-function policy helper
- `internal/store/tickets.go` — serialise the new fields
- `internal/store/project.go` — serialise the new config block
- `internal/svc/tickets.go` — `ArchiveTicket` / `UnarchiveTicket` svc methods
- `internal/svc/search.go` — `include_archived` post-filter
- `internal/svc/listing.go` (wherever `ListTickets` lives) — same filter
- `internal/domain/comment.go` — extend `CommentKind` enum with archive/unarchive
- `internal/mcptools/tools.go` — register `archive_ticket`, `unarchive_ticket`; thread `include_archived` through the three search-tool handlers + `list_tickets`
- `SPEC.md` — document archived state, search default behavior, and the decision matrix

Depends on T1 (the feedback store is what feeds the decision matrix).
