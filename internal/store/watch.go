package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceWindow is the coalescing window used by ProjectWatcher. Bursts of
// fsnotify events within this window collapse into a single emitted signal.
const debounceWindow = 50 * time.Millisecond

// ProjectWatcher emits a coalesced "something changed in this project" signal.
// The cache layer (T04) listens on Events and flips its Stale flag.
type ProjectWatcher struct {
	Slug   string
	Events chan struct{}

	w        *fsnotify.Watcher
	root     string // absolute path of the watched project dir
	stopOnce sync.Once
	stopCh   chan struct{}
}

// Close stops the watcher and closes Events. Safe to call multiple times.
func (p *ProjectWatcher) Close() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
		_ = p.w.Close()
	})
}

// WatchProject sets up a recursive fsnotify watcher on the given project dir,
// filters out `.staging/` and `.lock` events, and coalesces bursts. When
// fsnotify is disabled by config the returned watcher emits no events and
// Close() is a no-op-on-empty-state.
func (s *Store) WatchProject(slug string) (*ProjectWatcher, error) {
	root := s.projectDir(slug)
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("watch %s: %w", slug, err)
	}

	pw := &ProjectWatcher{
		Slug:   slug,
		Events: make(chan struct{}, 1),
		root:   root,
		stopCh: make(chan struct{}),
	}

	if !s.FsnotifyEnabled {
		// Caller still gets a closeable watcher; just no events.
		return pw, nil
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify new: %w", err)
	}
	pw.w = w

	if err := addWatchRecursive(w, root); err != nil {
		_ = w.Close()
		return nil, err
	}

	go pw.loop()
	return pw, nil
}

// loop pumps fsnotify events into the debounced Events channel.
func (p *ProjectWatcher) loop() {
	var (
		timer  *time.Timer
		timerC <-chan time.Time
		dirty  bool
	)

	emit := func() {
		// Non-blocking: a buffered channel of capacity 1 means consumers
		// see at most one pending signal; further bursts coalesce.
		select {
		case p.Events <- struct{}{}:
		default:
		}
	}

	scheduleEmit := func() {
		dirty = true
		if timer == nil {
			timer = time.NewTimer(debounceWindow)
			timerC = timer.C
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(debounceWindow)
		}
	}

	for {
		select {
		case <-p.stopCh:
			if timer != nil {
				timer.Stop()
			}
			close(p.Events)
			return
		case ev, ok := <-p.w.Events:
			if !ok {
				close(p.Events)
				return
			}
			if shouldIgnore(p.root, ev.Name) {
				continue
			}
			// New directories created → add a watch so we keep coverage.
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = p.w.Add(ev.Name)
				}
			}
			scheduleEmit()
		case <-timerC:
			if dirty {
				dirty = false
				emit()
			}
		case <-p.w.Errors:
			// fsnotify error channel — drop; we don't signal these to
			// callers. A persistent error will surface via Close path.
		}
	}
}

// addWatchRecursive registers root and every descendant directory.
func addWatchRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		return w.Add(path)
	})
}

// shouldIgnore returns true if an event path is one we deliberately filter:
// .staging/ tree, .lock files, or temp files our atomic-write helper produces.
func shouldIgnore(root, evPath string) bool {
	rel, err := filepath.Rel(root, evPath)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." {
		return false
	}
	if strings.HasPrefix(rel, ".staging/") || rel == ".staging" {
		return true
	}
	base := filepath.Base(rel)
	if base == fileLock {
		return true
	}
	if strings.HasPrefix(base, ".tmp-") {
		return true
	}
	return false
}
