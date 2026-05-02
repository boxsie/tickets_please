package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WalkComments iterates the comments subdir of a ticket directory in
// chronological order. The comment filename convention encodes the timestamp
// as a sortable prefix (`<created_at_compact>-<short-id>-<kind>.md`) so a
// simple string sort yields chronological order regardless of FS return order.
//
// fn receives the parsed CommentRecord (frontmatter) and the markdown body.
func (s *Store) WalkComments(ticketDir string, fn func(rec *CommentRecord, body string) error) error {
	dir := filepath.Join(ticketDir, dirComments)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read comments dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Comment markdown only; sidecar embeddings are skipped.
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		rec := &CommentRecord{}
		body, err := DecodeMarkdownInto(filepath.Join(dir, name), rec)
		if err != nil {
			return err
		}
		if err := fn(rec, body); err != nil {
			return err
		}
	}
	return nil
}
