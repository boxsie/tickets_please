package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// WalkPhases iterates every `projects/<slug>/phases/*/phase.yaml` in
// directory-name (which encodes the phase number prefix) order.
func (s *Store) WalkPhases(slug string, fn func(rec *PhaseRecord) error) error {
	dir := filepath.Join(s.projectDir(slug), dirPhases)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read phases dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name, filePhase)
		rec := &PhaseRecord{}
		if err := ReadYAML(path, rec); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
	return nil
}

// PhaseDir returns the absolute path to a phase's directory. The dirName is
// the on-disk `<NNN>-<slug>` folder name, which the cache layer derives from
// PhaseRecord.Number + PhaseRecord.Slug.
func (s *Store) PhaseDir(projectSlug, phaseDirName string) string {
	return filepath.Join(s.projectDir(projectSlug), dirPhases, phaseDirName)
}
