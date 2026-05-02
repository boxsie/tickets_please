package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// WalkProjects iterates every `projects/<slug>/project.yaml`, calling fn with
// the slug (the directory name) and the parsed record. Iteration is in slug
// alphabetical order. Stops on the first non-nil error returned by fn.
func (s *Store) WalkProjects(fn func(slug string, rec *ProjectRecord) error) error {
	entries, err := os.ReadDir(s.projectsDir())
	if err != nil {
		return fmt.Errorf("read projects dir: %w", err)
	}

	slugs := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == "" || e.Name()[0] == '.' {
			continue
		}
		slugs = append(slugs, e.Name())
	}
	sort.Strings(slugs)

	for _, slug := range slugs {
		path := filepath.Join(s.projectDir(slug), fileProject)
		rec := &ProjectRecord{}
		if err := ReadYAML(path, rec); err != nil {
			if os.IsNotExist(err) {
				// Project dir without project.yaml — skip in walks; integrity
				// check will surface this as a fatal error.
				continue
			}
			return err
		}
		if err := fn(slug, rec); err != nil {
			return err
		}
	}
	return nil
}

// ReadProject loads the ProjectRecord at projects/<slug>/project.yaml.
func (s *Store) ReadProject(slug string) (*ProjectRecord, error) {
	rec := &ProjectRecord{}
	path := filepath.Join(s.projectDir(slug), fileProject)
	if err := ReadYAML(path, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// ReadProjectSummary loads `summary.md` for the given slug.
func (s *Store) ReadProjectSummary(slug string) (string, error) {
	path := filepath.Join(s.projectDir(slug), fileSummary)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ProjectDir returns the absolute project directory path. Exported so the
// cache layer can use it for relative-path computations without re-deriving
// the layout.
func (s *Store) ProjectDir(slug string) string {
	return s.projectDir(slug)
}
