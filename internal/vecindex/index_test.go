package vecindex

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// normalize returns v scaled to unit L2 norm. Used to mirror what the
// embedding providers return.
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := 1.0 / math.Sqrt(sum)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out
}

func randVec(r *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(r.NormFloat64())
	}
	return normalize(v)
}

// --- Cosine -----------------------------------------------------------------

func TestCosine(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"length-mismatch", []float32{1, 0}, []float32{1, 0, 0}, 0},
		{"empty", []float32{}, []float32{}, 0},
		{"zero-norm-a", []float32{0, 0, 0}, []float32{1, 0, 0}, 0},
		{"zero-norm-b", []float32{1, 0, 0}, []float32{0, 0, 0}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cosine(tc.a, tc.b)
			if math.IsNaN(float64(got)) {
				t.Fatalf("cosine returned NaN")
			}
			if math.Abs(float64(got-tc.want)) > 1e-6 {
				t.Errorf("cosine = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- Search basics ----------------------------------------------------------

func TestUpsertSearchTopHit(t *testing.T) {
	idx := New()
	q := normalize([]float32{1, 2, 3, 4})
	idx.Upsert(Entry{ID: "a", Kind: KindTicketBody, Owner: "p1", Vec: q})
	idx.Upsert(Entry{ID: "b", Kind: KindTicketBody, Owner: "p1", Vec: normalize([]float32{4, 3, 2, 1})})

	hits := idx.Search(q, KindTicketBody, "p1", 0)
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	if hits[0].ID != "a" {
		t.Errorf("top hit = %q, want %q", hits[0].ID, "a")
	}
	if math.Abs(float64(hits[0].Score-1.0)) > 1e-5 {
		t.Errorf("top score = %v, want ~1.0", hits[0].Score)
	}
}

func TestSearchOwnerFilter(t *testing.T) {
	idx := New()
	q := normalize([]float32{1, 0, 0})
	idx.Upsert(Entry{ID: "foo-1", Kind: KindTicketBody, Owner: "foo", Vec: q})
	idx.Upsert(Entry{ID: "bar-1", Kind: KindTicketBody, Owner: "bar", Vec: q})
	idx.Upsert(Entry{ID: "foo-2", Kind: KindTicketBody, Owner: "foo", Vec: normalize([]float32{0, 1, 0})})

	hits := idx.Search(q, KindTicketBody, "foo", 10)
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2 (foo only)", len(hits))
	}
	for _, h := range hits {
		if h.ID == "bar-1" {
			t.Errorf("owner filter leaked: got %q", h.ID)
		}
	}
}

func TestSearchKindFilter(t *testing.T) {
	idx := New()
	q := normalize([]float32{1, 0, 0})
	idx.Upsert(Entry{ID: "body", Kind: KindTicketBody, Vec: q})
	idx.Upsert(Entry{ID: "comment", Kind: KindComment, Vec: q})
	idx.Upsert(Entry{ID: "summary", Kind: KindProjectSummary, Vec: q})

	hits := idx.Search(q, KindTicketBody, "", 10)
	if len(hits) != 1 || hits[0].ID != "body" {
		t.Fatalf("kind filter: got %v, want [body]", hits)
	}
}

func TestSearchNoOwnerFilter(t *testing.T) {
	idx := New()
	q := normalize([]float32{1, 0, 0})
	idx.Upsert(Entry{ID: "x", Kind: KindTicketBody, Owner: "p1", Vec: q})
	idx.Upsert(Entry{ID: "y", Kind: KindTicketBody, Owner: "p2", Vec: q})
	hits := idx.Search(q, KindTicketBody, "", 10)
	if len(hits) != 2 {
		t.Fatalf("no-owner-filter: got %d hits, want 2", len(hits))
	}
}

func TestDelete(t *testing.T) {
	idx := New()
	q := normalize([]float32{1, 0, 0})
	idx.Upsert(Entry{ID: "a", Kind: KindTicketBody, Vec: q})
	idx.Upsert(Entry{ID: "b", Kind: KindTicketBody, Vec: q})
	idx.Delete("a")
	if idx.Len() != 1 {
		t.Fatalf("Len = %d, want 1", idx.Len())
	}
	hits := idx.Search(q, KindTicketBody, "", 10)
	for _, h := range hits {
		if h.ID == "a" {
			t.Errorf("Delete left %q in index", h.ID)
		}
	}
	idx.Delete("missing") // no-op
}

func TestSearchLimitDefaults(t *testing.T) {
	idx := New()
	for i := 0; i < 25; i++ {
		v := normalize([]float32{float32(i + 1), 1, 1})
		idx.Upsert(Entry{ID: fmt.Sprintf("e-%02d", i), Kind: KindTicketBody, Vec: v})
	}
	q := normalize([]float32{1, 1, 1})

	if got := len(idx.Search(q, KindTicketBody, "", 0)); got != 10 {
		t.Errorf("limit=0 default: got %d, want 10", got)
	}
	if got := len(idx.Search(q, KindTicketBody, "", 999)); got != 25 {
		t.Errorf("limit=999 cap: got %d, want 25 (cap=50, only 25 entries)", got)
	}
}

func TestSearchLimitCap(t *testing.T) {
	idx := New()
	for i := 0; i < 60; i++ {
		v := normalize([]float32{float32(i + 1), 1, 1})
		idx.Upsert(Entry{ID: fmt.Sprintf("e-%02d", i), Kind: KindTicketBody, Vec: v})
	}
	q := normalize([]float32{1, 1, 1})
	if got := len(idx.Search(q, KindTicketBody, "", 100)); got != 50 {
		t.Errorf("limit cap: got %d, want 50", got)
	}
}

func TestSearchLimitGreaterThanEntries(t *testing.T) {
	idx := New()
	q := normalize([]float32{1, 0, 0})
	idx.Upsert(Entry{ID: "a", Kind: KindTicketBody, Vec: q})
	idx.Upsert(Entry{ID: "b", Kind: KindTicketBody, Vec: normalize([]float32{0, 1, 0})})

	hits := idx.Search(q, KindTicketBody, "", 50)
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	if hits[0].Score < hits[1].Score {
		t.Errorf("hits not sorted desc: %+v", hits)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	idx := New()
	idx.Upsert(Entry{ID: "a", Kind: KindTicketBody, Vec: normalize([]float32{1, 0})})
	if hits := idx.Search(nil, KindTicketBody, "", 10); hits != nil {
		t.Errorf("empty query: got %+v, want nil", hits)
	}
}

func TestSearchSkipsDimMismatch(t *testing.T) {
	idx := New()
	idx.Upsert(Entry{ID: "ok", Kind: KindTicketBody, Vec: normalize([]float32{1, 0, 0})})
	idx.Upsert(Entry{ID: "bad", Kind: KindTicketBody, Vec: normalize([]float32{1, 0})})

	hits := idx.Search(normalize([]float32{1, 0, 0}), KindTicketBody, "", 10)
	if len(hits) != 1 || hits[0].ID != "ok" {
		t.Fatalf("dim mismatch: got %+v, want [ok]", hits)
	}
}

func TestSnapshotAndLen(t *testing.T) {
	idx := New()
	if idx.Len() != 0 {
		t.Fatalf("fresh Len = %d, want 0", idx.Len())
	}
	for i := 0; i < 5; i++ {
		idx.Upsert(Entry{
			ID:   fmt.Sprintf("e-%d", i),
			Kind: KindComment,
			Vec:  normalize([]float32{float32(i + 1), 1}),
		})
	}
	if idx.Len() != 5 {
		t.Errorf("Len = %d, want 5", idx.Len())
	}
	snap := idx.Snapshot()
	if len(snap) != 5 {
		t.Errorf("Snapshot len = %d, want 5", len(snap))
	}
}

// --- Top-k correctness vs reference ----------------------------------------

func TestSearchTopKVsReference(t *testing.T) {
	const (
		dim = 32
		n   = 100
		k   = 7
	)
	r := rand.New(rand.NewSource(42))
	idx := New()
	all := make([]Entry, n)
	for i := 0; i < n; i++ {
		e := Entry{
			ID:   fmt.Sprintf("e-%03d", i),
			Kind: KindTicketBody,
			Vec:  randVec(r, dim),
		}
		idx.Upsert(e)
		all[i] = e
	}
	q := randVec(r, dim)

	// Reference: full sort.
	type scored struct {
		id    string
		score float32
	}
	ref := make([]scored, n)
	for i, e := range all {
		ref[i] = scored{e.ID, cosine(q, e.Vec)}
	}
	sort.SliceStable(ref, func(a, b int) bool {
		if ref[a].score != ref[b].score {
			return ref[a].score > ref[b].score
		}
		return ref[a].id < ref[b].id
	})

	hits := idx.Search(q, KindTicketBody, "", k)
	if len(hits) != k {
		t.Fatalf("got %d hits, want %d", len(hits), k)
	}
	for i := 0; i < k; i++ {
		if hits[i].ID != ref[i].id {
			t.Errorf("hit[%d] = %q (%.6f), ref = %q (%.6f)", i, hits[i].ID, hits[i].Score, ref[i].id, ref[i].score)
		}
		if math.Abs(float64(hits[i].Score-ref[i].score)) > 1e-6 {
			t.Errorf("hit[%d] score = %v, ref = %v", i, hits[i].Score, ref[i].score)
		}
	}
}

// --- Concurrency ------------------------------------------------------------

func TestConcurrentUpsertSearchDelete(t *testing.T) {
	const dim = 16
	idx := New()
	r := rand.New(rand.NewSource(7))

	// Seed with 50 entries so Search has something to chew on from t=0.
	for i := 0; i < 50; i++ {
		idx.Upsert(Entry{
			ID:   fmt.Sprintf("seed-%02d", i),
			Kind: KindTicketBody,
			Vec:  randVec(r, dim),
		})
	}
	q := randVec(r, dim)

	var (
		wg     sync.WaitGroup
		stop   atomic.Bool
		writes atomic.Int64
		reads  atomic.Int64
	)

	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			lr := rand.New(rand.NewSource(seed))
			i := 0
			for !stop.Load() {
				id := fmt.Sprintf("w%d-%d", seed, i)
				idx.Upsert(Entry{ID: id, Kind: KindTicketBody, Vec: randVec(lr, dim)})
				if i%3 == 0 {
					idx.Delete(id)
				}
				writes.Add(1)
				i++
			}
		}(int64(w + 1))
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				_ = idx.Search(q, KindTicketBody, "", 10)
				_ = idx.Snapshot()
				_ = idx.Len()
				reads.Add(1)
			}
		}()
	}

	// Let it churn briefly, then signal stop. Race detector catches any
	// missing locks; the test passes if no race fires and goroutines exit.
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) || writes.Load() < 1000 || reads.Load() < 1000 {
		runtime.Gosched()
	}
	stop.Store(true)
	wg.Wait()
}

// --- Sidecar persistence ----------------------------------------------------

func TestSidecarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "vec.embedding.json")

	r := rand.New(rand.NewSource(99))
	want := randVec(r, 768)

	in := Sidecar{
		Provider: "ollama",
		Model:    "bge-m3",
		Dim:      len(want),
		Vec:      want,
	}
	if err := WriteSidecar(path, in); err != nil {
		t.Fatalf("WriteSidecar: %v", err)
	}
	got, err := ReadSidecar(path)
	if err != nil {
		t.Fatalf("ReadSidecar: %v", err)
	}
	if got.Provider != in.Provider {
		t.Errorf("Provider = %q, want %q", got.Provider, in.Provider)
	}
	if got.Model != in.Model {
		t.Errorf("Model = %q, want %q", got.Model, in.Model)
	}
	if got.Dim != in.Dim {
		t.Errorf("Dim = %d, want %d", got.Dim, in.Dim)
	}
	if len(got.Vec) != len(want) {
		t.Fatalf("dim = %d, want %d", len(got.Vec), len(want))
	}
	for i := range want {
		if math.Abs(float64(got.Vec[i]-want[i])) > 1e-6 {
			t.Fatalf("idx %d: got %v, want %v", i, got.Vec[i], want[i])
		}
	}
}

func TestSidecarReadMissing(t *testing.T) {
	if _, err := ReadSidecar(filepath.Join(t.TempDir(), "nope.embedding.json")); err == nil {
		t.Errorf("ReadSidecar on missing file: err = nil, want error")
	}
}

func TestSidecarReadGarbage(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.embedding.json")
	if err := os.WriteFile(bad, []byte("{not an array}"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := ReadSidecar(bad); err == nil {
		t.Errorf("ReadSidecar on garbage: err = nil, want error")
	}
}

func TestSidecarOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vec.embedding.json")
	if err := WriteSidecar(path, Sidecar{Provider: "p", Model: "m", Dim: 3, Vec: []float32{1, 2, 3}}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteSidecar(path, Sidecar{Provider: "p", Model: "m", Dim: 4, Vec: []float32{4, 5, 6, 7}}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, err := ReadSidecar(path)
	if err != nil {
		t.Fatalf("ReadSidecar: %v", err)
	}
	if len(got.Vec) != 4 || got.Vec[0] != 4 || got.Vec[3] != 7 {
		t.Errorf("overwrite: got %v, want [4 5 6 7]", got.Vec)
	}
}
