package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// WalkProjects invokes fn for the single project hosted by this Store, if any.
// Post-flatten a Store is single-project: there's at most one `project.yaml`
// at the data-dir root. The function is preserved (rather than collapsed into
// a `ReadProject` call) so callers can stay shape-stable across the v0.1 →
// v0.2 transition. ENOENT means "no project here yet" and is silent.
func (s *Store) WalkProjects(fn func(slug string, rec *ProjectRecord) error) error {
	rec := &ProjectRecord{}
	path := filepath.Join(s.Root, fileProject)
	if err := ReadYAML(path, rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return fn(rec.Slug, rec)
}

// ReadProject loads the ProjectRecord at `<Root>/project.yaml`. The slug
// argument is validated against the on-disk record — a mismatch surfaces as
// fs.ErrNotExist so cache lookups for an unknown slug behave as if the project
// is absent (consistent with the pre-flatten directory-not-found case).
func (s *Store) ReadProject(slug string) (*ProjectRecord, error) {
	rec := &ProjectRecord{}
	path := filepath.Join(s.Root, fileProject)
	if err := ReadYAML(path, rec); err != nil {
		return nil, err
	}
	if slug != "" && rec.Slug != slug {
		return nil, fs.ErrNotExist
	}
	return rec, nil
}

// ReadProjectSummary loads `<Root>/summary.md`. Slug is informational; an
// existing summary.md belongs to whichever project owns the data dir.
func (s *Store) ReadProjectSummary(_ string) (string, error) {
	path := filepath.Join(s.Root, fileSummary)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ProjectDir returns the absolute path to the project's directory. With the
// flat layout this is just `s.Root`; callers (notably the cache layer and
// integrity check) should treat slug as informational.
func (s *Store) ProjectDir(_ string) string {
	return s.Root
}
