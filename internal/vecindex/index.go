// Package vecindex is the in-memory vector index that backs SearchService.
//
// It is a leaf-level package: stdlib only, no project imports. The index
// stores normalized []float32 vectors keyed by id and answers brute-force
// cosine top-k queries via container/heap. For the project's scale
// (thousands of 768-dim vectors) brute force is plenty; if we ever need
// HNSW, the public API here is the seam.
package vecindex

import (
	"container/heap"
	"sort"
	"sync"
)

// Kind tags an Entry with the source domain it represents. Search filters
// by Kind so a single Index instance can technically hold mixed kinds, but
// in practice each Index is one kind (per-project body, learnings, etc.).
type Kind int

const (
	KindUnspecified Kind = iota
	KindProjectSummary
	KindTicketBody
	KindTicketLearnings
	KindComment
)

// Entry is one vector in the index.
type Entry struct {
	ID    string  // source row id (project_id / ticket_id / comment_id)
	Kind  Kind    // the domain of the source row
	Owner string  // project slug; empty for global indexes
	Vec   []float32
}

// Hit is one search result. Score is cosine similarity in [-1, 1] with 1.0
// meaning identical and 0.0 meaning orthogonal.
type Hit struct {
	ID    string
	Score float32
}

// Index is a goroutine-safe map of id → Entry with a top-k cosine search.
type Index struct {
	mu      sync.RWMutex
	entries map[string]Entry
}

// Search default/cap for the limit parameter.
const (
	defaultLimit = 10
	maxLimit     = 50
)

// New returns an empty Index ready for use.
func New() *Index {
	return &Index{entries: make(map[string]Entry)}
}

// Upsert adds or replaces an entry by ID. The caller's slice is stored
// directly (no copy) — callers should not mutate Vec after handing it off.
func (i *Index) Upsert(e Entry) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.entries[e.ID] = e
}

// Delete removes the entry with id from the index. No-op if absent.
func (i *Index) Delete(id string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.entries, id)
}

// RemoveByOwner drops every entry whose Owner matches owner and returns the
// count removed. Used by the project mount registry so an evicted/unmounted
// project's vectors don't keep showing up in cross-project searches. owner
// must be non-empty — passing "" is a no-op (avoids accidentally wiping
// entries from indexes that haven't been tagged yet).
func (i *Index) RemoveByOwner(owner string) int {
	if owner == "" {
		return 0
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	var removed int
	for id, e := range i.entries {
		if e.Owner == owner {
			delete(i.entries, id)
			removed++
		}
	}
	return removed
}

// Len returns the current entry count.
func (i *Index) Len() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.entries)
}

// Snapshot returns a read-only copy of all entries (for diagnostics). Vec
// slices are shared with the index; do not mutate them.
func (i *Index) Snapshot() []Entry {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]Entry, 0, len(i.entries))
	for _, e := range i.entries {
		out = append(out, e)
	}
	return out
}

// Search returns the top-`limit` entries (after kind/owner filtering) ranked
// by cosine similarity to query, descending. limit=0 defaults to 10 and is
// capped at 50. ownerFilter == "" disables the owner check. Entries whose
// Vec length doesn't match query are silently skipped.
func (i *Index) Search(query []float32, kind Kind, ownerFilter string, limit int) []Hit {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if len(query) == 0 {
		return nil
	}

	i.mu.RLock()
	defer i.mu.RUnlock()

	h := &minHeap{}
	heap.Init(h)
	for _, e := range i.entries {
		if e.Kind != kind {
			continue
		}
		if ownerFilter != "" && e.Owner != ownerFilter {
			continue
		}
		if len(e.Vec) != len(query) {
			continue
		}
		score := cosine(query, e.Vec)
		if h.Len() < limit {
			heap.Push(h, Hit{ID: e.ID, Score: score})
			continue
		}
		// Heap root holds the lowest-scoring candidate currently in the
		// top-k. If the new score beats it, swap.
		if score > (*h)[0].Score {
			(*h)[0] = Hit{ID: e.ID, Score: score}
			heap.Fix(h, 0)
		}
	}

	out := make([]Hit, h.Len())
	for n := len(out) - 1; n >= 0; n-- {
		out[n] = heap.Pop(h).(Hit)
	}
	// Ensure stable ordering for ties (by ID ascending) — heap pop already
	// emits descending Score, so just stabilise within equal scores.
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// minHeap is a min-heap of Hit by Score, used as a top-k bounded heap so the
// root is always the weakest currently-kept hit and easy to evict.
type minHeap []Hit

func (h minHeap) Len() int            { return len(h) }
func (h minHeap) Less(i, j int) bool  { return h[i].Score < h[j].Score }
func (h minHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *minHeap) Push(x any)         { *h = append(*h, x.(Hit)) }
func (h *minHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
