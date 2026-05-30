package store

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"tickets_please/internal/domain"
)

const (
	fileFeedback        = "feedback.yaml"
	feedbackVersion     = 1
	feedbackReasonLimit = 10
	feedbackReasonChars = 500
)

// feedbackFile is the on-disk shape of `<project>/.tickets_please/feedback.yaml`.
type feedbackFile struct {
	Version int                            `yaml:"version"`
	Entries map[string]feedbackEntryRecord `yaml:"entries,omitempty"`
}

// feedbackEntryRecord is the on-disk shape of a single entry's aggregate. Kept
// distinct from domain.FeedbackRecord so YAML tags don't leak into the domain
// type and so the on-disk shape can evolve independently.
type feedbackEntryRecord struct {
	Likes          int       `yaml:"likes,omitempty"`
	Dislikes       int       `yaml:"dislikes,omitempty"`
	LastFeedbackAt time.Time `yaml:"last_feedback_at,omitempty"`
	LastUsedAt     time.Time `yaml:"last_used_at,omitempty"`
	Retrievals     int       `yaml:"retrievals,omitempty"`
	Reasons        []string  `yaml:"reasons,omitempty"`
}

// FeedbackStore is the per-project feedback aggregate store. One instance per
// mounted project; not safe to construct without a backing *Store + slug, so
// callers go through LoadFeedback.
//
// Cross-process serialisation uses the per-project advisory flock
// (LockProject); in-process readers are protected by a RWMutex so search hot
// paths can call Get without contention.
type FeedbackStore struct {
	store *Store
	slug  string
	path  string

	mu      sync.RWMutex
	entries map[domain.EntryKey]domain.FeedbackRecord
}

// LoadFeedback opens (or initialises) the feedback store at
// `<s.Root>/feedback.yaml`. A missing file is treated as an empty store;
// callers can mutate immediately and the file appears on first write. A
// version mismatch on an existing file returns a wrapped error so the mount
// can refuse to come up rather than silently dropping data we don't know how
// to read.
func LoadFeedback(s *Store, slug string) (*FeedbackStore, error) {
	if s == nil {
		return nil, errors.New("feedback: nil store")
	}
	if slug == "" {
		return nil, errors.New("feedback: empty slug")
	}
	fb := &FeedbackStore{
		store:   s,
		slug:    slug,
		path:    filepath.Join(s.Root, fileFeedback),
		entries: map[domain.EntryKey]domain.FeedbackRecord{},
	}
	if err := fb.reload(); err != nil {
		return nil, err
	}
	return fb, nil
}

// reload reads feedback.yaml from disk into the in-memory map. Missing file
// is a clean empty load (no error).
func (f *FeedbackStore) reload() error {
	data, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) || os.IsNotExist(err) {
			f.mu.Lock()
			f.entries = map[domain.EntryKey]domain.FeedbackRecord{}
			f.mu.Unlock()
			return nil
		}
		return fmt.Errorf("feedback: read %s: %w", f.path, err)
	}
	var file feedbackFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("feedback: parse %s: %w", f.path, err)
	}
	if file.Version == 0 {
		// Pre-version file or hand-edited without the header — treat as v1 so
		// a user grepping the repo can extend the file by hand without bumping
		// version explicitly.
		file.Version = feedbackVersion
	}
	if file.Version != feedbackVersion {
		return fmt.Errorf("feedback: %s has version %d, this binary supports version %d",
			f.path, file.Version, feedbackVersion)
	}
	next := make(map[domain.EntryKey]domain.FeedbackRecord, len(file.Entries))
	for k, v := range file.Entries {
		next[domain.EntryKey(k)] = domain.FeedbackRecord{
			Likes:          v.Likes,
			Dislikes:       v.Dislikes,
			LastFeedbackAt: v.LastFeedbackAt,
			LastUsedAt:     v.LastUsedAt,
			Retrievals:     v.Retrievals,
			Reasons:        append([]string(nil), v.Reasons...),
		}
	}
	f.mu.Lock()
	f.entries = next
	f.mu.Unlock()
	return nil
}

// Get returns the record for key. The bool reports whether a record exists —
// callers that want zero-defaults should ignore it.
func (f *FeedbackStore) Get(key domain.EntryKey) (domain.FeedbackRecord, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	rec, ok := f.entries[key]
	if !ok {
		return domain.FeedbackRecord{}, false
	}
	return cloneFeedbackRecord(rec), true
}

// Walk invokes fn for each entry in key-sorted order. fn returning false stops
// the walk early. Reads a coherent snapshot — concurrent mutations during the
// walk don't affect the values fn sees.
func (f *FeedbackStore) Walk(fn func(domain.EntryKey, domain.FeedbackRecord) bool) error {
	f.mu.RLock()
	keys := make([]string, 0, len(f.entries))
	snap := make(map[domain.EntryKey]domain.FeedbackRecord, len(f.entries))
	for k, v := range f.entries {
		keys = append(keys, string(k))
		snap[k] = v
	}
	f.mu.RUnlock()
	sort.Strings(keys)
	for _, k := range keys {
		if !fn(domain.EntryKey(k), cloneFeedbackRecord(snap[domain.EntryKey(k)])) {
			return nil
		}
	}
	return nil
}

// RecordRating bumps the like or dislike counter for key, appends an optional
// reason (capped at the last 10), and writes the store atomically. Acquires
// the per-project flock for cross-process safety.
func (f *FeedbackStore) RecordRating(ctx context.Context, key domain.EntryKey, rating domain.Rating, reason string) error {
	if key == "" {
		return errors.New("feedback: empty entry key")
	}
	if rating != domain.RatingLike && rating != domain.RatingDislike {
		return fmt.Errorf("feedback: invalid rating %q", rating)
	}
	if len(reason) > feedbackReasonChars {
		reason = reason[:feedbackReasonChars]
	}
	return f.mutate(ctx, func(now time.Time, m map[domain.EntryKey]domain.FeedbackRecord) {
		rec := m[key]
		if rating == domain.RatingLike {
			rec.Likes++
		} else {
			rec.Dislikes++
		}
		rec.LastFeedbackAt = now
		if reason != "" {
			rec.Reasons = append(rec.Reasons, reason)
			if len(rec.Reasons) > feedbackReasonLimit {
				rec.Reasons = rec.Reasons[len(rec.Reasons)-feedbackReasonLimit:]
			}
		}
		m[key] = rec
	})
}

// RecordRetrieval bumps the retrievals counter and last_used_at for every
// supplied key, in a single store write. Used by the search hot path to feed
// the W3 archive policy.
func (f *FeedbackStore) RecordRetrieval(ctx context.Context, keys []domain.EntryKey) error {
	if len(keys) == 0 {
		return nil
	}
	return f.mutate(ctx, func(now time.Time, m map[domain.EntryKey]domain.FeedbackRecord) {
		for _, key := range keys {
			if key == "" {
				continue
			}
			rec := m[key]
			rec.Retrievals++
			rec.LastUsedAt = now
			m[key] = rec
		}
	})
}

// Delete drops key's record entirely. Used when the underlying entity
// (ticket, comment) is deleted so feedback.yaml doesn't accumulate references
// to entities that no longer exist. No-op when the key is absent.
func (f *FeedbackStore) Delete(ctx context.Context, key domain.EntryKey) error {
	if key == "" {
		return nil
	}
	return f.mutate(ctx, func(_ time.Time, m map[domain.EntryKey]domain.FeedbackRecord) {
		delete(m, key)
	})
}

// DeleteMany is a batched Delete used on cascades (ticket deletion clearing
// learning + comment refs in one write).
func (f *FeedbackStore) DeleteMany(ctx context.Context, keys []domain.EntryKey) error {
	if len(keys) == 0 {
		return nil
	}
	return f.mutate(ctx, func(_ time.Time, m map[domain.EntryKey]domain.FeedbackRecord) {
		for _, k := range keys {
			if k == "" {
				continue
			}
			delete(m, k)
		}
	})
}

// mutate is the shared load-modify-write path. It acquires the per-project
// flock, reloads the on-disk file (so concurrent writers from other processes
// are picked up), applies mut to a fresh map, and writes atomically.
func (f *FeedbackStore) mutate(ctx context.Context, mut func(now time.Time, m map[domain.EntryKey]domain.FeedbackRecord)) error {
	if f == nil {
		return errors.New("feedback: nil store")
	}
	return f.store.WithProjectLock(ctx, f.slug, func() error {
		if err := f.reload(); err != nil {
			return err
		}
		f.mu.Lock()
		// Work on a copy so a panic in mut doesn't half-apply.
		next := make(map[domain.EntryKey]domain.FeedbackRecord, len(f.entries))
		for k, v := range f.entries {
			next[k] = cloneFeedbackRecord(v)
		}
		f.mu.Unlock()
		mut(time.Now().UTC(), next)
		if err := writeFeedbackFile(f.path, next); err != nil {
			return err
		}
		f.mu.Lock()
		f.entries = next
		f.mu.Unlock()
		return nil
	})
}

// writeFeedbackFile serialises entries to YAML and writes atomically. Empty
// stores still emit a header so a grep-er sees the file shape rather than
// nothing.
func writeFeedbackFile(path string, entries map[domain.EntryKey]domain.FeedbackRecord) error {
	out := feedbackFile{
		Version: feedbackVersion,
		Entries: make(map[string]feedbackEntryRecord, len(entries)),
	}
	for k, v := range entries {
		out.Entries[string(k)] = feedbackEntryRecord{
			Likes:          v.Likes,
			Dislikes:       v.Dislikes,
			LastFeedbackAt: v.LastFeedbackAt,
			LastUsedAt:     v.LastUsedAt,
			Retrievals:     v.Retrievals,
			Reasons:        append([]string(nil), v.Reasons...),
		}
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("feedback: marshal: %w", err)
	}
	header := []byte("# tickets_please feedback aggregates — likes/dislikes/retrievals per search result.\n" +
		"# Schema: version + map keyed by `<kind>:<id>` (see internal/domain/feedback.go).\n" +
		"# Edit by hand only if you know what you're doing; the MCP `rate_search_result` tool is the supported path.\n")
	full := append(header, body...)
	return writeFileAtomic(path, full)
}

// cloneFeedbackRecord deep-copies a record so the in-memory snapshot can't be
// mutated through a Get caller's hand.
func cloneFeedbackRecord(r domain.FeedbackRecord) domain.FeedbackRecord {
	if r.Reasons == nil {
		return r
	}
	out := r
	out.Reasons = append([]string(nil), r.Reasons...)
	return out
}
