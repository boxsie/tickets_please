package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"tickets_please/internal/domain"
)

// op is the typed operation interface a StageOp accumulates. Each op is
// applied as a single syscall under the held flock — see SPEC.md
// §Atomicity for the exact semantics.
type op interface {
	apply(rootDir string) error
	touchedPath(rootDir string) string
}

type writeOp struct {
	rel        string
	stagedPath string // absolute path inside .staging/<op-id>/
}

func (o writeOp) apply(rootDir string) error {
	dest := filepath.Join(rootDir, o.rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", dest, err)
	}
	return os.Rename(o.stagedPath, dest)
}
func (o writeOp) touchedPath(rootDir string) string {
	return filepath.Join(rootDir, o.rel)
}

type renameOp struct{ from, to string }

func (o renameOp) apply(rootDir string) error {
	src := filepath.Join(rootDir, o.from)
	dst := filepath.Join(rootDir, o.to)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", dst, err)
	}
	return os.Rename(src, dst)
}
func (o renameOp) touchedPath(rootDir string) string {
	return filepath.Join(rootDir, o.to)
}

type removeOp struct{ rel string }

func (o removeOp) apply(rootDir string) error {
	return os.RemoveAll(filepath.Join(rootDir, o.rel))
}
func (o removeOp) touchedPath(rootDir string) string {
	return filepath.Join(rootDir, o.rel)
}

// StageOp accumulates an ordered list of typed ops that get applied
// atomically-per-op under a flock at Commit time. See the package doc and
// SPEC.md §Atomicity for the rationale and contract.
type StageOp struct {
	OpID  string
	dir   string // absolute path of .staging/<op-id>/
	store *Store
	ops   []op

	committed bool
	aborted   bool
}

// BeginOp starts a new staged operation, creating its `.staging/<op-id>/`
// directory.
func (s *Store) BeginOp() (*StageOp, error) {
	id := uuid.NewString()
	dir := filepath.Join(s.stagingDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir staging: %w", err)
	}
	return &StageOp{
		OpID:  id,
		dir:   dir,
		store: s,
	}, nil
}

// Write stages a file at .staging/<op-id>/<relPath>. The write happens
// immediately (sync'd to disk) so a crash before Commit leaves staged
// content the integrity check can surface.
func (o *StageOp) Write(relPath string, content []byte) error {
	if err := o.checkOpen(); err != nil {
		return err
	}
	if err := validateRel(relPath); err != nil {
		return err
	}
	staged := filepath.Join(o.dir, relPath)
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return fmt.Errorf("mkdir staged parent: %w", err)
	}
	f, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open staged %s: %w", staged, err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return fmt.Errorf("write staged %s: %w", staged, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync staged %s: %w", staged, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close staged %s: %w", staged, err)
	}
	o.ops = append(o.ops, writeOp{rel: relPath, stagedPath: staged})
	return nil
}

// RenameDir registers an in-place os.Rename to be applied at Commit. Both
// paths are relative to Store.Root.
func (o *StageOp) RenameDir(fromRel, toRel string) error {
	if err := o.checkOpen(); err != nil {
		return err
	}
	if err := validateRel(fromRel); err != nil {
		return err
	}
	if err := validateRel(toRel); err != nil {
		return err
	}
	o.ops = append(o.ops, renameOp{from: fromRel, to: toRel})
	return nil
}

// RemovePath registers an os.RemoveAll to be applied at Commit. Path is
// relative to Store.Root and may be a file or a tree.
func (o *StageOp) RemovePath(relPath string) error {
	if err := o.checkOpen(); err != nil {
		return err
	}
	if err := validateRel(relPath); err != nil {
		return err
	}
	o.ops = append(o.ops, removeOp{rel: relPath})
	return nil
}

// Abort cleans up the staging dir without applying any ops.
func (o *StageOp) Abort() {
	if o.committed || o.aborted {
		return
	}
	o.aborted = true
	_ = os.RemoveAll(o.dir)
}

// Commit acquires the requested flock, applies the ops in declared order,
// removes the staging dir, optionally produces an auto-commit, and releases
// the lock. Failures mid-apply leave a partial on-disk state — the integrity
// check at next startup surfaces residual `.staging/<op-id>/` if cleanup was
// reached, or the partial dir contents otherwise.
func (o *StageOp) Commit(ctx context.Context, scope LockScope, agent *domain.Agent, summary string) error {
	if o.committed {
		return errors.New("StageOp already committed")
	}
	if o.aborted {
		return errors.New("StageOp aborted")
	}
	o.committed = true

	return o.store.withLock(ctx, scope, func() error {
		touched := make([]string, 0, len(o.ops))
		for _, op := range o.ops {
			if err := op.apply(o.store.Root); err != nil {
				// Leave .staging/<op-id>/ in place so integrity sees it.
				return fmt.Errorf("apply op: %w", err)
			}
			touched = append(touched, op.touchedPath(o.store.Root))
		}
		if err := os.RemoveAll(o.dir); err != nil {
			o.store.Logger.Warn("staging cleanup failed", "op_id", o.OpID, "err", err)
		}
		if o.store.AutoCommit && agent != nil && len(touched) > 0 {
			if err := o.store.gitCommit(ctx, touched, agent, summary); err != nil {
				// Auto-commit failures don't fail the StageOp itself —
				// audit-trail is best-effort.
				o.store.Logger.Warn("auto-commit failed", "op_id", o.OpID, "err", err)
			}
		}
		return nil
	})
}

// checkOpen returns an error if the StageOp is already committed or aborted.
func (o *StageOp) checkOpen() error {
	if o.committed {
		return errors.New("StageOp already committed")
	}
	if o.aborted {
		return errors.New("StageOp aborted")
	}
	return nil
}

// validateRel rejects empty paths, absolute paths, and any path that contains
// a parent-traversal segment. We never want absolute paths leaking into
// stored data.
func validateRel(rel string) error {
	if rel == "" {
		return errors.New("empty relative path")
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("absolute path not allowed: %s", rel)
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") || strings.Contains(clean, string(filepath.Separator)+"..") {
		return fmt.Errorf("traversal not allowed: %s", rel)
	}
	return nil
}
