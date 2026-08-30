package svc

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestRegistry_LoadEmpty: a missing registry file is not an error and yields
// an empty slice. Lets a fresh Service.New start cleanly.
func TestRegistry_LoadEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := loadMountRegistry(dir)
	if err != nil {
		t.Fatalf("loadMountRegistry: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

// TestRegistry_LoadEmptyDataRoot: empty dataRoot is a no-op so stdio mode
// without a centralised root doesn't error.
func TestRegistry_LoadEmptyDataRoot(t *testing.T) {
	got, err := loadMountRegistry("")
	if err != nil {
		t.Fatalf("loadMountRegistry(\"\"): %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

// TestRegistry_SaveLoad: a roundtrip preserves the path set + sort order.
func TestRegistry_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	in := []string{"/b/repo", "/a/repo", "/c/repo"}
	if err := saveMountRegistry(dir, in); err != nil {
		t.Fatalf("saveMountRegistry: %v", err)
	}
	got, err := loadMountRegistry(dir)
	if err != nil {
		t.Fatalf("loadMountRegistry: %v", err)
	}
	want := []string{"/a/repo", "/b/repo", "/c/repo"}
	if !equalStrings(got, want) {
		t.Errorf("got %v, want %v (sorted)", got, want)
	}
	// File should exist on disk in the expected location.
	if _, err := os.Stat(filepath.Join(dir, registryFilename)); err != nil {
		t.Errorf("registry file not written: %v", err)
	}
}

// TestRegistry_SaveDedupes: duplicate paths in the input collapse on save.
func TestRegistry_SaveDedupes(t *testing.T) {
	dir := t.TempDir()
	in := []string{"/a/repo", "/b/repo", "/a/repo"}
	if err := saveMountRegistry(dir, in); err != nil {
		t.Fatalf("saveMountRegistry: %v", err)
	}
	got, _ := loadMountRegistry(dir)
	if len(got) != 2 {
		t.Errorf("got %v, want 2 unique entries", got)
	}
}

// TestRegistry_SaveSkipsNonAbs: relative paths are dropped silently. We
// never want a relative path in the registry — it would re-mount against
// the wrong working dir on next boot.
func TestRegistry_SaveSkipsNonAbs(t *testing.T) {
	dir := t.TempDir()
	in := []string{"/abs/repo", "relative/repo", "../parent/repo"}
	if err := saveMountRegistry(dir, in); err != nil {
		t.Fatalf("saveMountRegistry: %v", err)
	}
	got, _ := loadMountRegistry(dir)
	if len(got) != 1 || got[0] != "/abs/repo" {
		t.Errorf("got %v, want [/abs/repo]", got)
	}
}

// TestRegistry_AddIdempotent: calling add twice with the same path yields
// the same on-disk state. No duplicates leak in.
func TestRegistry_AddIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := addToMountRegistry(dir, "/a/repo"); err != nil {
		t.Fatalf("add 1: %v", err)
	}
	if err := addToMountRegistry(dir, "/a/repo"); err != nil {
		t.Fatalf("add 2: %v", err)
	}
	got, _ := loadMountRegistry(dir)
	if len(got) != 1 || got[0] != "/a/repo" {
		t.Errorf("got %v, want [/a/repo]", got)
	}
}

// TestRegistry_AddRejectsRelative: relative paths return an error rather than
// silently no-oping, so callers that pass bad paths catch the bug.
func TestRegistry_AddRejectsRelative(t *testing.T) {
	dir := t.TempDir()
	if err := addToMountRegistry(dir, "relative/repo"); err == nil {
		t.Errorf("expected error for relative path")
	}
}

// TestRegistry_RemovePresent: removing an existing path drops it from disk.
func TestRegistry_RemovePresent(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"/a/repo", "/b/repo", "/c/repo"} {
		if err := addToMountRegistry(dir, p); err != nil {
			t.Fatalf("add %s: %v", p, err)
		}
	}
	if err := removeFromMountRegistry(dir, "/b/repo"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, _ := loadMountRegistry(dir)
	want := []string{"/a/repo", "/c/repo"}
	if !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestRegistry_RemoveAbsent: removing a path that isn't there is a no-op,
// not an error. Lets DeleteProject call remove unconditionally.
func TestRegistry_RemoveAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := addToMountRegistry(dir, "/a/repo"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := removeFromMountRegistry(dir, "/never-added"); err != nil {
		t.Fatalf("remove absent: %v", err)
	}
	got, _ := loadMountRegistry(dir)
	if len(got) != 1 || got[0] != "/a/repo" {
		t.Errorf("got %v, want [/a/repo]", got)
	}
}

// TestRegistry_AtomicWrite: there's no half-written tmp file left behind
// after a normal save. Catches a regression where writeFileAtomic forgot to
// clean up tmp files on success.
func TestRegistry_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	if err := saveMountRegistry(dir, []string{"/a/repo"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || (len(e.Name()) > 4 && e.Name()[:4] == ".tmp") {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
