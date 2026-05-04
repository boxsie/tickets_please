package svc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"tickets_please/internal/store"
)

// registry.go: persistent record of which project repos this server has been
// told to mount. Lets the sidebar survive a restart.
//
// Format: <DataRoot>/registry.yaml — a tiny dumb file alongside agents/.
// The on-disk project.yaml in each repo is the source of truth; the registry
// is a hint that says "these are the absolute paths the user has loaded".
//
//   paths:
//     - /home/dan/code/tickets_please
//     - /home/dan/code/some-other-repo
//   updated_at: 2026-05-04T18:14:00Z
//
// Reads are cheap (small YAML), writes happen only on RegisterProjectMount
// success and DeleteProject commit-success. Both go through the same
// load → mutate → atomic-save path so concurrent callers can't corrupt the
// file even though we don't take a flock — the host data root has a single
// writer (this process).

const registryFilename = "registry.yaml"

// mountRegistry is the on-disk shape. Kept as a private type because no other
// package needs to construct one.
type mountRegistry struct {
	Paths     []string  `yaml:"paths"`
	UpdatedAt time.Time `yaml:"updated_at"`
}

// loadMountRegistry returns the absolute paths recorded in
// <dataRoot>/registry.yaml. Missing file → empty slice + nil error (the
// service has never been told to mount anything yet).
//
// Returned paths are deduped and filtered to absolute paths. Stale paths
// whose underlying directory has vanished are kept in the returned slice
// (RegisterProjectMount handles the actual existence check + log-and-skip
// at re-mount time, so the caller can surface "the repo at X is gone"
// instead of the registry silently pruning).
func loadMountRegistry(dataRoot string) ([]string, error) {
	if dataRoot == "" {
		return nil, nil
	}
	path := filepath.Join(dataRoot, registryFilename)
	var reg mountRegistry
	if err := store.ReadYAML(path, &reg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("registry: read %s: %w", path, err)
	}
	out := make([]string, 0, len(reg.Paths))
	seen := map[string]struct{}{}
	for _, p := range reg.Paths {
		if !filepath.IsAbs(p) {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}

// saveMountRegistry writes paths to <dataRoot>/registry.yaml atomically.
// updated_at is set to time.Now().UTC(). Paths are sorted for stable diffs.
//
// Empty dataRoot is a no-op (stdio mode without a centralised root has
// nothing to persist to).
func saveMountRegistry(dataRoot string, paths []string) error {
	if dataRoot == "" {
		return nil
	}
	clean := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		clean = append(clean, p)
	}
	sort.Strings(clean)
	reg := mountRegistry{Paths: clean, UpdatedAt: time.Now().UTC()}
	return store.WriteYAMLAtomic(filepath.Join(dataRoot, registryFilename), reg)
}

// addToMountRegistry records path. Idempotent — calling twice with the same
// path leaves the registry in the same state (modulo updated_at).
func addToMountRegistry(dataRoot, path string) error {
	if dataRoot == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("registry: add: path %q must be absolute", path)
	}
	current, err := loadMountRegistry(dataRoot)
	if err != nil {
		return err
	}
	for _, p := range current {
		if p == path {
			return nil
		}
	}
	return saveMountRegistry(dataRoot, append(current, path))
}

// removeFromMountRegistry drops path. No-op when path is absent.
func removeFromMountRegistry(dataRoot, path string) error {
	if dataRoot == "" {
		return nil
	}
	current, err := loadMountRegistry(dataRoot)
	if err != nil {
		return err
	}
	out := current[:0]
	changed := false
	for _, p := range current {
		if p == path {
			changed = true
			continue
		}
		out = append(out, p)
	}
	if !changed {
		return nil
	}
	return saveMountRegistry(dataRoot, out)
}
