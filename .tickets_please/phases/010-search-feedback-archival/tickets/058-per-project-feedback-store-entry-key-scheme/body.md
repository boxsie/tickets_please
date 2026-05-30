## Goal

Land the on-disk substrate that the rest of the phase builds on: a per-project feedback record keyed by a stable entry key, with read/write/walk semantics that match the rest of the store.

## Scope

### Entry-key scheme

Define `domain.EntryKey` as the string form `<kind>:<id>` where kind ∈ {`ticket`, `learning`, `comment`}. Add helpers:
- `domain.TicketEntryKey(ticketID) string`
- `domain.LearningEntryKey(ticketID) string` — completion learnings are 1:1 with their parent ticket, so the same id; the kind discriminator distinguishes them in the store
- `domain.CommentEntryKey(commentID) string`
- `domain.ParseEntryKey(s string) (kind, id string, ok bool)`

Place in `internal/domain/feedback.go`.

### Record type

```go
type FeedbackRecord struct {
    Likes           int       `yaml:"likes"`
    Dislikes        int       `yaml:"dislikes"`
    LastFeedbackAt  time.Time `yaml:"last_feedback_at,omitempty"`
    LastUsedAt      time.Time `yaml:"last_used_at,omitempty"`
    Retrievals      int       `yaml:"retrievals"` // how many times this entry has appeared in any search top-k (W3 uses this)
    Reasons         []string  `yaml:"reasons,omitempty"` // append-only, last ~10 kept
}
```

### Store

`internal/store/feedback.go` — single YAML file at `.tickets_please/feedback.yaml` per project:

```yaml
version: 1
entries:
  "learning:51153b49-...":
    likes: 3
    dislikes: 1
    last_feedback_at: 2026-05-30T12:00:00Z
    last_used_at:    2026-05-30T13:14:22Z
    retrievals: 7
  "ticket:abc...":
    ...
```

Operations on the store type:
- `Load(projectPath) (*FeedbackStore, error)` — file may not exist (treat as empty); validates `version: 1`
- `Get(key EntryKey) (FeedbackRecord, bool)`
- `RecordRating(key EntryKey, rating Rating, reason string) error` — atomic increment + write
- `RecordRetrieval(keys []EntryKey) error` — bumps `retrievals` and `last_used_at` for each, single write
- `Walk(fn func(EntryKey, FeedbackRecord) bool) error`
- `Delete(key EntryKey) error` — used when the underlying ticket/comment is deleted

Persistence: load-modify-write under a per-project advisory file lock (use the existing flock helpers in `internal/store`). Atomic write via temp-file + rename, matching `project.yaml` / `summary.md` patterns.

### Wire into Service

Mount hydrate (`hydrateMount` in `internal/svc/`) loads the feedback store onto `ProjectMount`. Add a `Feedback() *FeedbackStore` accessor. No svc methods yet — those land in T2/T4; this ticket only exposes the store.

### Lifecycle hooks

When a ticket or comment is deleted, also delete its feedback entry. Hook into existing `DeleteTicket` and the (audit-logged) comment-delete path if one exists — grep first; if comments are truly immutable and never deleted, skip the comment side.

### Tests

- Round-trip a store with a handful of entries; assert YAML shape and atomic write.
- `RecordRating` increments correctly under concurrent calls (use `t.Parallel` + `errgroup`).
- `Walk` enumerates in deterministic order (sort by key for repeatable test output).
- Missing-file load returns an empty store, not an error.
- `version: 2` load returns a clear error (forward-compat guard).

## Out of scope

- The MCP tool that drives `RecordRating` — that's T2.
- Surfacing entry keys in search responses — that's T3.
- Any ranking change — that's W2.

## Critical files

- `internal/domain/feedback.go` (new)
- `internal/store/feedback.go` (new)
- `internal/svc/mounts.go` — hydrate the store onto the mount
- `internal/cache/projectcache.go` — if the cache holds the mount, make sure the feedback store is reloaded on cache invalidation
- `.tickets_please/feedback.yaml` — new file; add an explanatory comment header so anyone grepping the repo can see the format
- `.gitignore` — **do NOT** add feedback.yaml (it should be git-tracked, unlike embedding sidecars)
