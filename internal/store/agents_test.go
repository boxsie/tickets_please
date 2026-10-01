package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tickets_please/internal/domain"
)

func newTestAgentStore(t *testing.T) *AgentStore {
	t.Helper()
	as, err := NewAgentStore(t.TempDir(), 1)
	if err != nil {
		t.Fatalf("NewAgentStore: %v", err)
	}
	return as
}

func agentRec(id, key string, expiresAt time.Time) *AgentRecord {
	return &AgentRecord{ID: id, Key: key, Name: id, CreatedAt: expiresAt.Add(-time.Hour), ExpiresAt: expiresAt}
}

func walkIDs(t *testing.T, walk func(func(*AgentRecord) error) error) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	if err := walk(func(rec *AgentRecord) error { ids[rec.ID] = true; return nil }); err != nil {
		t.Fatalf("walk: %v", err)
	}
	return ids
}

func TestRegisterAgent_KeyIndexGuardsActiveKey(t *testing.T) {
	as := newTestAgentStore(t)
	ctx := context.Background()
	now := time.Now()

	if err := as.RegisterAgent(ctx, agentRec("a", "claude:x", now.Add(time.Hour))); err != nil {
		t.Fatalf("register a: %v", err)
	}
	err := as.RegisterAgent(ctx, agentRec("b", "claude:x", now.Add(time.Hour)))
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("register b with active key: got %v, want ErrAlreadyExists", err)
	}
	if err := as.RegisterAgent(ctx, agentRec("c", "claude:y", now.Add(time.Hour))); err != nil {
		t.Fatalf("register c with other key: %v", err)
	}

	// Once a's session has expired the key is free again, and the index moves
	// on to the new holder.
	if err := as.WriteAgentRecord(agentRec("a", "claude:x", now.Add(-time.Minute))); err != nil {
		t.Fatalf("expire a: %v", err)
	}
	if err := as.RegisterAgent(ctx, agentRec("d", "claude:x", now.Add(time.Hour))); err != nil {
		t.Fatalf("register d after expiry: %v", err)
	}
	if err := as.RegisterAgent(ctx, agentRec("e", "claude:x", now.Add(time.Hour))); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("register e while d active: got %v, want ErrAlreadyExists", err)
	}
}

func TestArchiveExpired_MovesOnlyLongExpired(t *testing.T) {
	as := newTestAgentStore(t)
	ctx := context.Background()
	now := time.Now()
	grace := 24 * time.Hour

	for _, rec := range []*AgentRecord{
		agentRec("live", "k:live", now.Add(time.Hour)),
		agentRec("recent", "k:recent", now.Add(-time.Hour)),
		agentRec("old", "k:old", now.Add(-48*time.Hour)),
	} {
		// Register with a far-future expiry so the index is written, then
		// rewrite the real expiry in place.
		orig := rec.ExpiresAt
		rec.ExpiresAt = now.Add(time.Hour)
		if err := as.RegisterAgent(ctx, rec); err != nil {
			t.Fatalf("register %s: %v", rec.ID, err)
		}
		rec.ExpiresAt = orig
		if err := as.WriteAgentRecord(rec); err != nil {
			t.Fatalf("rewrite %s: %v", rec.ID, err)
		}
	}

	report, err := as.ArchiveExpired(ctx, now, grace)
	if err != nil {
		t.Fatalf("ArchiveExpired: %v", err)
	}
	if report.Archived != 1 {
		t.Fatalf("archived = %d, want 1", report.Archived)
	}

	live := walkIDs(t, as.WalkAgents)
	if !live["live"] || !live["recent"] || live["old"] {
		t.Fatalf("live walk = %v, want live+recent only", live)
	}
	all := walkIDs(t, as.WalkAllAgents)
	if !all["old"] || len(all) != 3 {
		t.Fatalf("all walk = %v, want all three", all)
	}

	// Attribution still resolves for the archived record.
	rec, err := as.ReadAgent("old")
	if err != nil || rec.Key != "k:old" {
		t.Fatalf("ReadAgent(old) = %v, %v", rec, err)
	}
	if _, err := as.ReadAgent("nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ReadAgent(nope) = %v, want ErrNotFound", err)
	}

	// Its key-index entry went with it; the live ones' stayed.
	if _, err := os.Stat(as.keyPath("k:old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old key index still present: %v", err)
	}
	if _, err := os.Stat(as.keyPath("k:live")); err != nil {
		t.Fatalf("live key index missing: %v", err)
	}

	// A second pass is a no-op.
	report, err = as.ArchiveExpired(ctx, now, grace)
	if err != nil || report.Archived != 0 || report.Indexed != 0 {
		t.Fatalf("second pass = %+v, %v; want no-op", report, err)
	}
}

func TestArchiveExpired_BackfillsIndexForLegacyLiveRecords(t *testing.T) {
	as := newTestAgentStore(t)
	ctx := context.Background()
	now := time.Now()

	// A record written before the key index existed: file only, no index.
	if err := as.WriteAgentRecord(agentRec("legacy", "claude:old-build", now.Add(time.Hour))); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(as.Root, dirAgentKeys)); err != nil {
		t.Fatalf("key index dir: %v", err)
	}

	report, err := as.ArchiveExpired(ctx, now, 24*time.Hour)
	if err != nil {
		t.Fatalf("ArchiveExpired: %v", err)
	}
	if report.Indexed != 1 {
		t.Fatalf("indexed = %d, want 1", report.Indexed)
	}
	err = as.RegisterAgent(ctx, agentRec("new", "claude:old-build", now.Add(time.Hour)))
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("register over backfilled key: got %v, want ErrAlreadyExists", err)
	}
}
