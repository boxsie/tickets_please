package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// newFeedbackTestStore returns a *Store rooted at a temp dir, plus the slug
// used for the per-project flock. The flat layout means feedback.yaml lives
// at <tempdir>/feedback.yaml.
func newFeedbackTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(config.Config{DataDir: dir, LockTimeoutSeconds: 2})
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return s, "fbtest"
}

func TestLoadFeedback_MissingFileIsEmpty(t *testing.T) {
	s, slug := newFeedbackTestStore(t)
	fb, err := LoadFeedback(s, slug)
	if err != nil {
		t.Fatalf("LoadFeedback: %v", err)
	}
	if _, ok := fb.Get(domain.TicketEntryKey("abc")); ok {
		t.Fatalf("empty store should not contain any entry")
	}
	count := 0
	_ = fb.Walk(func(domain.EntryKey, domain.FeedbackRecord) bool { count++; return true })
	if count != 0 {
		t.Fatalf("walk over empty store: got %d entries, want 0", count)
	}
	if _, err := os.Stat(filepath.Join(s.Root, fileFeedback)); !os.IsNotExist(err) {
		t.Fatalf("Load should not create the file; stat err = %v", err)
	}
}

func TestRecordRating_PersistsAndIncrements(t *testing.T) {
	s, slug := newFeedbackTestStore(t)
	fb, err := LoadFeedback(s, slug)
	if err != nil {
		t.Fatalf("LoadFeedback: %v", err)
	}
	key := domain.LearningEntryKey("ticket-1")
	ctx := context.Background()

	if err := fb.RecordRating(ctx, key, domain.RatingLike, "useful"); err != nil {
		t.Fatalf("RecordRating like: %v", err)
	}
	if err := fb.RecordRating(ctx, key, domain.RatingLike, ""); err != nil {
		t.Fatalf("RecordRating like 2: %v", err)
	}
	if err := fb.RecordRating(ctx, key, domain.RatingDislike, "misled me"); err != nil {
		t.Fatalf("RecordRating dislike: %v", err)
	}

	rec, ok := fb.Get(key)
	if !ok {
		t.Fatalf("expected record present")
	}
	if rec.Likes != 2 {
		t.Errorf("Likes = %d, want 2", rec.Likes)
	}
	if rec.Dislikes != 1 {
		t.Errorf("Dislikes = %d, want 1", rec.Dislikes)
	}
	if len(rec.Reasons) != 2 || rec.Reasons[0] != "useful" || rec.Reasons[1] != "misled me" {
		t.Errorf("Reasons = %v, want [useful misled me]", rec.Reasons)
	}
	if rec.LastFeedbackAt.IsZero() {
		t.Errorf("LastFeedbackAt should be set")
	}

	// Round-trip via a fresh load to confirm persistence.
	fb2, err := LoadFeedback(s, slug)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	rec2, ok := fb2.Get(key)
	if !ok || rec2.Likes != 2 || rec2.Dislikes != 1 {
		t.Errorf("after reload: rec = %+v ok=%v", rec2, ok)
	}
}

func TestRecordRetrieval_BumpsAllKeys(t *testing.T) {
	s, slug := newFeedbackTestStore(t)
	fb, _ := LoadFeedback(s, slug)
	ctx := context.Background()

	keys := []domain.EntryKey{
		domain.TicketEntryKey("a"),
		domain.LearningEntryKey("b"),
		domain.CommentEntryKey("c"),
	}
	if err := fb.RecordRetrieval(ctx, keys); err != nil {
		t.Fatalf("RecordRetrieval: %v", err)
	}
	if err := fb.RecordRetrieval(ctx, keys); err != nil {
		t.Fatalf("RecordRetrieval second: %v", err)
	}
	for _, k := range keys {
		rec, ok := fb.Get(k)
		if !ok {
			t.Errorf("missing record for %q", k)
			continue
		}
		if rec.Retrievals != 2 {
			t.Errorf("%q retrievals = %d, want 2", k, rec.Retrievals)
		}
		if rec.LastUsedAt.IsZero() {
			t.Errorf("%q LastUsedAt should be set", k)
		}
	}
}

func TestRecordRating_ConcurrentIncrementsDontLose(t *testing.T) {
	s, slug := newFeedbackTestStore(t)
	fb, _ := LoadFeedback(s, slug)
	ctx := context.Background()
	key := domain.TicketEntryKey("concurrent")

	const N = 25
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if err := fb.RecordRating(ctx, key, domain.RatingLike, ""); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent RecordRating: %v", err)
	}
	rec, ok := fb.Get(key)
	if !ok || rec.Likes != N {
		t.Fatalf("Likes after %d concurrent calls = %d (ok=%v), want %d", N, rec.Likes, ok, N)
	}
}

func TestWalk_DeterministicSortedOrder(t *testing.T) {
	s, slug := newFeedbackTestStore(t)
	fb, _ := LoadFeedback(s, slug)
	ctx := context.Background()
	for _, k := range []domain.EntryKey{
		domain.TicketEntryKey("z"),
		domain.LearningEntryKey("a"),
		domain.CommentEntryKey("m"),
		domain.TicketEntryKey("b"),
	} {
		if err := fb.RecordRating(ctx, k, domain.RatingLike, ""); err != nil {
			t.Fatalf("RecordRating: %v", err)
		}
	}
	var seen []string
	_ = fb.Walk(func(k domain.EntryKey, _ domain.FeedbackRecord) bool {
		seen = append(seen, string(k))
		return true
	})
	want := []string{"comment:m", "learning:a", "ticket:b", "ticket:z"}
	if len(seen) != len(want) {
		t.Fatalf("walk len = %d, want %d (seen=%v)", len(seen), len(want), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("walk[%d] = %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestReasonsCappedAtTen(t *testing.T) {
	s, slug := newFeedbackTestStore(t)
	fb, _ := LoadFeedback(s, slug)
	ctx := context.Background()
	key := domain.TicketEntryKey("reasoned")
	for i := 0; i < 12; i++ {
		reason := strings.Repeat("x", i+1)
		if err := fb.RecordRating(ctx, key, domain.RatingLike, reason); err != nil {
			t.Fatalf("RecordRating: %v", err)
		}
	}
	rec, _ := fb.Get(key)
	if len(rec.Reasons) != 10 {
		t.Fatalf("Reasons len = %d, want 10", len(rec.Reasons))
	}
	// Oldest two ("x" and "xx") should have been evicted; first kept is "xxx".
	if rec.Reasons[0] != strings.Repeat("x", 3) {
		t.Errorf("oldest kept reason = %q, want %q", rec.Reasons[0], strings.Repeat("x", 3))
	}
	if rec.Reasons[9] != strings.Repeat("x", 12) {
		t.Errorf("newest reason = %q, want %q", rec.Reasons[9], strings.Repeat("x", 12))
	}
}

func TestLoadFeedback_VersionMismatch(t *testing.T) {
	s, slug := newFeedbackTestStore(t)
	path := filepath.Join(s.Root, fileFeedback)
	if err := os.WriteFile(path, []byte("version: 99\nentries: {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := LoadFeedback(s, slug)
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected version-mismatch error, got %v", err)
	}
}

func TestDelete_DropsEntry(t *testing.T) {
	s, slug := newFeedbackTestStore(t)
	fb, _ := LoadFeedback(s, slug)
	ctx := context.Background()
	key := domain.CommentEntryKey("doomed")
	if err := fb.RecordRating(ctx, key, domain.RatingLike, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok := fb.Get(key); !ok {
		t.Fatalf("seed didn't land")
	}
	if err := fb.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := fb.Get(key); ok {
		t.Fatalf("Delete didn't drop the entry")
	}
}

func TestDeleteMany_BatchDrop(t *testing.T) {
	s, slug := newFeedbackTestStore(t)
	fb, _ := LoadFeedback(s, slug)
	ctx := context.Background()
	a := domain.TicketEntryKey("a")
	b := domain.LearningEntryKey("a")
	c := domain.TicketEntryKey("survivor")
	for _, k := range []domain.EntryKey{a, b, c} {
		_ = fb.RecordRating(ctx, k, domain.RatingLike, "")
	}
	if err := fb.DeleteMany(ctx, []domain.EntryKey{a, b}); err != nil {
		t.Fatalf("DeleteMany: %v", err)
	}
	if _, ok := fb.Get(a); ok {
		t.Errorf("a should be gone")
	}
	if _, ok := fb.Get(b); ok {
		t.Errorf("b should be gone")
	}
	if _, ok := fb.Get(c); !ok {
		t.Errorf("c should remain")
	}
}

func TestWriteAtomic_NoTempLeftovers(t *testing.T) {
	s, slug := newFeedbackTestStore(t)
	fb, _ := LoadFeedback(s, slug)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = fb.RecordRating(ctx, domain.TicketEntryKey("k"), domain.RatingLike, "")
	}
	ents, err := os.ReadDir(s.Root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestParseEntryKey_Roundtrip(t *testing.T) {
	cases := []struct {
		key      domain.EntryKey
		wantKind domain.EntryKind
		wantID   string
	}{
		{domain.TicketEntryKey("abc"), domain.EntryKindTicket, "abc"},
		{domain.LearningEntryKey("xyz"), domain.EntryKindLearning, "xyz"},
		{domain.CommentEntryKey("123"), domain.EntryKindComment, "123"},
	}
	for _, c := range cases {
		gotKind, gotID, ok := domain.ParseEntryKey(string(c.key))
		if !ok {
			t.Errorf("ParseEntryKey(%q) failed", c.key)
			continue
		}
		if gotKind != c.wantKind || gotID != c.wantID {
			t.Errorf("ParseEntryKey(%q) = (%q, %q), want (%q, %q)",
				c.key, gotKind, gotID, c.wantKind, c.wantID)
		}
	}
	for _, bad := range []string{"", "ticket:", ":id", "weird:id", "no-colon"} {
		if _, _, ok := domain.ParseEntryKey(bad); ok {
			t.Errorf("ParseEntryKey(%q) unexpectedly ok", bad)
		}
	}
}
