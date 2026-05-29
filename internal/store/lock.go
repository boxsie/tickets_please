package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LockScope identifies which flock a StageOp.Commit acquires. Use
// LockGlobal for cross-project mutations (project create/delete) and
// LockProject(slug) for project-scoped mutations.
type LockScope struct {
	// global is true when the global root lock is targeted.
	global bool
	// slug is the project slug for per-project locks.
	slug string
}

// LockGlobal targets the root `<data_dir>/.lock` file.
var LockGlobal = LockScope{global: true}

// LockProject targets `<data_dir>/projects/<slug>/.lock`.
func LockProject(slug string) LockScope {
	return LockScope{slug: slug}
}

// resolve returns the absolute path to the lock file backing this scope, and
// ensures the parent directory exists.
func (l LockScope) resolve(s *Store) (string, error) {
	if l.global {
		return filepath.Join(s.Root, fileLock), nil
	}
	if l.slug == "" {
		return "", errors.New("empty project slug for project lock")
	}
	dir := s.projectDir(l.slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir project dir: %w", err)
	}
	return filepath.Join(dir, fileLock), nil
}

// describe is used in timeout error messages.
func (l LockScope) describe() string {
	if l.global {
		return "global"
	}
	return "project " + l.slug
}

// WithProjectLock acquires a per-project exclusive flock for the lifetime of
// fn. The data dir's `projects/<slug>/.lock` file is created if missing.
// Lock acquisition retries with 50ms polling until cfg.LockTimeoutSeconds
// elapses, at which point a "lock contention" error is returned.
func (s *Store) WithProjectLock(ctx context.Context, slug string, fn func() error) error {
	return s.withLock(ctx, LockProject(slug), fn)
}

// WithGlobalLock is the cross-project equivalent — used for project list
// mutations (create, delete).
func (s *Store) WithGlobalLock(ctx context.Context, fn func() error) error {
	return s.withLock(ctx, LockGlobal, fn)
}

// withLock is the shared retry-poll implementation for both scopes.
func (s *Store) withLock(ctx context.Context, scope LockScope, fn func() error) error {
	path, err := scope.resolve(s)
	if err != nil {
		return err
	}
	f, err := acquireFlock(ctx, path, time.Duration(s.LockTimeoutSeconds)*time.Second)
	if err != nil {
		return err
	}
	defer func() {
		// Closing the fd releases the lock — POSIX flock and Windows
		// LockFileEx both release on handle close.
		_ = f.Close()
	}()
	return fn()
}

// acquireFlock opens path (creating it if missing) and tries to take an
// exclusive advisory lock, non-blocking, in a 50ms poll loop until it
// succeeds, the context is canceled, or timeout elapses. The platform
// implementation lives in lock_unix.go (flock) / lock_windows.go (LockFileEx);
// both release the lock when the returned *os.File is closed.
