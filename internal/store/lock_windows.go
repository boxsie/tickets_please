//go:build windows

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// acquireFlock (Windows) takes an exclusive whole-file lock via LockFileEx with
// LOCKFILE_FAIL_IMMEDIATELY, polling every 50ms until success/timeout/cancel —
// the same advisory-lock contract the Unix flock path provides. Closing the
// returned *os.File releases the lock (the kernel drops LockFileEx ranges when
// the handle closes).
func acquireFlock(ctx context.Context, path string, timeout time.Duration) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}

	h := windows.Handle(f.Fd())
	// Lock the first byte; every process locks the identical region so the
	// lock is mutually exclusive even on a zero-length file.
	const lockBytes = 1
	deadline := time.Now().Add(timeout)
	for {
		err := windows.LockFileEx(
			h,
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, lockBytes, 0, new(windows.Overlapped),
		)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_IO_PENDING) {
			_ = f.Close()
			return nil, fmt.Errorf("LockFileEx %s: %w", path, err)
		}
		// Contention — wait and retry.
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lock contention on %s (held > %s)", path, timeout)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
