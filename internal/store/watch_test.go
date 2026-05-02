package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchProject_DebouncesBurst(t *testing.T) {
	s := freshStore(t)
	if err := os.MkdirAll(s.projectDir("foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := s.WatchProject("foo")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Drain any startup signal.
	drain := func(d time.Duration) {
		select {
		case <-w.Events:
		case <-time.After(d):
		}
	}
	drain(50 * time.Millisecond)

	// Burst of 10 writes should coalesce into a single Events emission.
	for i := 0; i < 10; i++ {
		path := filepath.Join(s.projectDir("foo"), "f"+string(rune('a'+i)))
		_ = os.WriteFile(path, []byte("x"), 0o644)
	}

	select {
	case <-w.Events:
	case <-time.After(2 * time.Second):
		t.Fatal("expected coalesced Events signal")
	}

	// Should not emit a second one in the next debounce window when no
	// further writes happen.
	select {
	case <-w.Events:
		t.Fatal("got unexpected second Events; debounce should coalesce")
	case <-time.After(150 * time.Millisecond):
		// good
	}
}

func TestWatchProject_FiltersStagingAndLock(t *testing.T) {
	s := freshStore(t)
	if err := os.MkdirAll(s.projectDir("foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := s.WatchProject("foo")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Drain any startup events.
	select {
	case <-w.Events:
	case <-time.After(50 * time.Millisecond):
	}

	// Touch the lock file — must NOT signal.
	lock := filepath.Join(s.projectDir("foo"), ".lock")
	_ = os.WriteFile(lock, []byte{}, 0o644)

	select {
	case <-w.Events:
		t.Fatal(".lock writes should be filtered")
	case <-time.After(150 * time.Millisecond):
		// good
	}
}
