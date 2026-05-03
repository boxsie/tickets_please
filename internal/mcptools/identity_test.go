package mcptools

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"tickets_please/internal/config"
)

func makeSession(key string) *Session {
	return &Session{
		AgentID:   "agent-" + key,
		AgentKey:  key,
		AgentName: "Test " + key,
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

// TestRegistry_RegisterAndGet verifies that a registered session is retrievable.
func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry(config.Config{})
	sess := makeSession("abc")
	if err := r.Register("sid-1", sess); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get("sid-1")
	if !ok {
		t.Fatal("Get returned not-ok after Register")
	}
	if got.AgentKey != "abc" {
		t.Errorf("AgentKey: got %q want %q", got.AgentKey, "abc")
	}
}

// TestRegistry_GetMissing verifies that Get on an unknown session returns (nil, false).
func TestRegistry_GetMissing(t *testing.T) {
	r := NewRegistry(config.Config{})
	got, ok := r.Get("no-such-id")
	if ok {
		t.Error("Get should return false for unknown session")
	}
	if got != nil {
		t.Errorf("Get should return nil for unknown session, got %+v", got)
	}
}

// TestRegistry_RegisterOverwrites verifies that re-registering the same session
// ID replaces the previous entry.
func TestRegistry_RegisterOverwrites(t *testing.T) {
	r := NewRegistry(config.Config{})
	first := makeSession("first")
	second := makeSession("second")

	if err := r.Register("sid", first); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	if err := r.Register("sid", second); err != nil {
		t.Fatalf("Register second: %v", err)
	}
	got, ok := r.Get("sid")
	if !ok {
		t.Fatal("Get returned not-ok")
	}
	if got.AgentKey != "second" {
		t.Errorf("overwrite failed: got AgentKey=%q want %q", got.AgentKey, "second")
	}
	if r.Len() != 1 {
		t.Errorf("Len: got %d want 1 after overwrite", r.Len())
	}
}

// TestRegistry_RemoveAndTouch verifies that Remove drops the entry and Touch on
// a missing ID is a no-op (not a panic or error).
func TestRegistry_RemoveAndTouch(t *testing.T) {
	r := NewRegistry(config.Config{})
	sess := makeSession("xyz")
	if err := r.Register("sid", sess); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if r.Len() != 1 {
		t.Fatalf("Len before remove: %d", r.Len())
	}
	r.Remove("sid")
	if r.Len() != 0 {
		t.Errorf("Len after remove: got %d want 0", r.Len())
	}
	_, ok := r.Get("sid")
	if ok {
		t.Error("Get after Remove should return false")
	}

	// Touch on a missing session must be a no-op.
	r.Touch("no-such-id") // must not panic
}

// TestRegistry_ConcurrentRegisterGet exercises concurrent Register + Get on
// N goroutines and checks that Len converges and there are no data races.
// Run with -race.
func TestRegistry_ConcurrentRegisterGet(t *testing.T) {
	const n = 50
	r := NewRegistry(config.Config{})
	var wg sync.WaitGroup

	// Half the goroutines register; half read. They all use distinct keys so
	// the final Len should equal n/2.
	for i := 0; i < n/2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("sid-%d", idx)
			_ = r.Register(key, makeSession(fmt.Sprintf("k%d", idx)))
		}(i)
	}
	for i := 0; i < n/2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("sid-%d", idx)
			_, _ = r.Get(key)
		}(i)
	}
	wg.Wait()

	// After all writers are done, Len must equal n/2.
	if got := r.Len(); got != n/2 {
		t.Errorf("Len after concurrent writes: got %d want %d", got, n/2)
	}
}
