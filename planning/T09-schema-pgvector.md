---
id: T09
title: Vector index (in-memory)
status: TODO
owner: ""
depends_on: [T02]
parallelizable_with: [T15]
wave: 2
files:
  - internal/vecindex/index.go
  - internal/vecindex/persist.go
  - internal/vecindex/cosine.go
estimate: small
stretch: false
---

# T09 — Vector index (in-memory)

## Scope

The in-memory data structure that backs `SearchService`. Brute-force cosine top-k over `[]float32` vectors loaded from `*.embedding.json` sidecar files. Per-project slices owned by `LoadedProject`; resident slices for `learnings_index` and `projects_summary_index`.

**In:** `internal/vecindex/` package with `Index` type, sidecar load/save helpers, hot-path `Search` returning ranked hits.

**Out:** No embedding generation (T08), no worker pipeline (T10), no gRPC handlers (T11). Just the index data structure and IO.

## Files

- `internal/vecindex/index.go` — `Index` type, Upsert/Delete/Search
- `internal/vecindex/persist.go` — `Read(path)` / `Write(path, []float32)` for `*.embedding.json` sidecars (JSON array)
- `internal/vecindex/cosine.go` — pure-Go cosine similarity with bounds checks
- `internal/vecindex/index_test.go` — unit tests including a determinism test on a small fixture

## Details

### Types

```go
type Kind int
const (
    KindUnspecified Kind = iota
    KindProjectSummary
    KindTicketBody
    KindTicketLearnings
    KindComment
)

type Entry struct {
    ID    string  // source row id (project_id / ticket_id / comment_id)
    Kind  Kind
    Owner string  // project slug; empty for global indexes
    Vec   []float32
}

type Hit struct {
    ID    string
    Score float32  // 1.0 = identical, 0.0 = orthogonal
}

type Index struct {
    entries map[string]Entry
    mu      sync.RWMutex
}

func New() *Index
func (i *Index) Upsert(e Entry)
func (i *Index) Delete(id string)
func (i *Index) Search(query []float32, kind Kind, ownerFilter string, limit int) []Hit
func (i *Index) Snapshot() []Entry  // read-only copy, for diagnostics
func (i *Index) Len() int
```

### Search semantics

- `query` length must equal entry vec length; otherwise return error (panic in tests, log in prod).
- Filter: only consider entries where `e.Kind == kind`.
- If `ownerFilter != ""`, also filter `e.Owner == ownerFilter`.
- Compute cosine for each remaining entry, keep a `container/heap`-backed top-k.
- Sort hits descending by score.
- `limit` defaults to 10 when 0; cap at 50.

### Persistence

```go
func ReadSidecar(path string) ([]float32, error)   // parse JSON array
func WriteSidecar(path string, vec []float32) error // atomic write via temp+rename
```

`WriteSidecar` writes via `MkdirTemp` next to the destination, then `os.Rename` — same pattern as `internal/store`. JSON format:

```json
[0.0123, -0.0456, ...]
```

No metadata, no length header — the dim is implicit.

### Loading at startup / project-load

- **Resident indexes** (`learnings_index`, `projects_summary_index`) load at server boot: walk `projects/*/tickets/*/learnings.embedding.json` and `projects/*/summary.embedding.json` respectively, calling `Upsert` for each.
- **Per-project indexes** load when `LoadProject` (T04) materializes a project: walk `projects/<slug>/tickets/*/body.embedding.json` and `projects/<slug>/tickets/*/comments/*.embedding.json`.

Loading errors per-file are warn-logged and skipped, not fatal — a bad sidecar is fixable by re-embedding.

### Concurrency

- `Search` takes `mu.RLock`.
- `Upsert` / `Delete` take `mu.Lock`.
- The embedding worker (T10) calls `Upsert` after writing a sidecar; per-project mutations happen behind the project's per-project lock (T04).

## Acceptance criteria

- [ ] `Upsert` then `Search` returns the upserted entry as the top hit when the query is identical to its vec (score very close to 1.0).
- [ ] `Search` with `kind=TicketBody, ownerFilter="foo"` does not return entries owned by other projects.
- [ ] `Delete` removes an id; subsequent `Search` no longer surfaces it.
- [ ] `WriteSidecar` then `ReadSidecar` round-trips without precision loss above ~1e-6 (JSON is text but float32 → string → float32 is stable enough for cosine).
- [ ] Concurrent `Upsert` + `Search` (run with `-race`) produces no data races.
- [ ] Top-k is correct on a fixture of ~100 entries vs a brute-force reference implementation.
- [ ] `Search` with `limit > len(entries)` returns all entries sorted by score, no panic.

## Notes

See **Vector search index** in [`../SPEC.md`](../SPEC.md). HNSW is a possible future swap — keep `Index` an interface-friendly struct so `coder/hnsw` could slot in without touching callers.

T10 owns *when* to call `Upsert`; this ticket only owns the data structure. T11 reads via `Search` and assembles `*Hit` result structs.
