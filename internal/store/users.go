package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tickets_please/internal/domain"
)

// dirUsers is the subdirectory under the UserStore root holding
// per-user yaml files. Sibling of `agents/` and `memberships/`.
const dirUsers = "users"

// UserStore is the filesystem handle for the central user registry. Like
// AgentStore it is decoupled from per-repo *Store so a long-running server
// can share one UserStore across many project Stores. Sharing a Root with
// AgentStore is fine — they coexist in `<Root>/users/` vs `<Root>/agents/`
// and use the same global `.lock` file for cross-process serialisation.
//
// On-disk layout:
//
//	<Root>/users/<user-uuid>.yaml
//	<Root>/.lock
type UserStore struct {
	Root               string
	LockTimeoutSeconds int
}

// NewUserStore resolves root to an absolute path, creates `<root>/users/`
// and `<root>/.staging/`, and returns the UserStore.
func NewUserStore(root string, lockTimeoutSeconds int) (*UserStore, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve user store root: %w", err)
	}
	for _, sub := range []string{dirUsers, dirStaging} {
		if err := os.MkdirAll(filepath.Join(abs, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir user store %s: %w", sub, err)
		}
	}
	lts := lockTimeoutSeconds
	if lts <= 0 {
		lts = 10
	}
	return &UserStore{Root: abs, LockTimeoutSeconds: lts}, nil
}

func (u *UserStore) usersDir() string {
	return filepath.Join(u.Root, dirUsers)
}

func (u *UserStore) withGlobalLock(ctx context.Context, fn func() error) error {
	path := filepath.Join(u.Root, fileLock)
	f, err := acquireFlock(ctx, path, time.Duration(u.LockTimeoutSeconds)*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return fn()
}

// WalkUsers iterates `users/*.yaml` in filename order, calling fn with each
// parsed UserRecord. Returning a non-nil error from fn aborts the walk.
func (u *UserStore) WalkUsers(fn func(rec *UserRecord) error) error {
	entries, err := os.ReadDir(u.usersDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read users dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		rec := &UserRecord{}
		if err := ReadYAML(filepath.Join(u.usersDir(), name), rec); err != nil {
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

// ReadUser loads `users/<id>.yaml`. Returns domain.ErrNotFound when no such
// file exists.
func (u *UserStore) ReadUser(id string) (*UserRecord, error) {
	rec := &UserRecord{}
	path := filepath.Join(u.usersDir(), id+".yaml")
	if err := ReadYAML(path, rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: user %s", domain.ErrNotFound, id)
		}
		return nil, err
	}
	return rec, nil
}

// WriteUser persists the record atomically. Used for both initial insert and
// subsequent updates (e.g. LastLoginAt bump, linking a second OAuth provider).
// Callers MUST hold the global lock around any read-modify-write sequence to
// avoid lost updates.
func (u *UserStore) WriteUser(rec *UserRecord) error {
	if rec.ID == "" {
		return fmt.Errorf("WriteUser: empty id")
	}
	path := filepath.Join(u.usersDir(), rec.ID+".yaml")
	return WriteYAMLAtomic(path, rec)
}

// FindUserByOAuthSubject returns the user whose provider-subject field
// matches `sub`. `provider` is "github" (matched against GitHubLogin) or
// "google" (matched against GoogleSub). Returns domain.ErrNotFound when no
// user is linked to that subject. An unknown provider returns a clear error.
func (u *UserStore) FindUserByOAuthSubject(provider, sub string) (*UserRecord, error) {
	if sub == "" {
		return nil, fmt.Errorf("FindUserByOAuthSubject: empty subject")
	}
	var match *UserRecord
	err := u.WalkUsers(func(rec *UserRecord) error {
		switch provider {
		case "github":
			if rec.GitHubLogin != nil && *rec.GitHubLogin == sub {
				match = rec
			}
		case "google":
			if rec.GoogleSub != nil && *rec.GoogleSub == sub {
				match = rec
			}
		default:
			return fmt.Errorf("FindUserByOAuthSubject: unknown provider %q", provider)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if match == nil {
		return nil, fmt.Errorf("%w: user with %s subject %q", domain.ErrNotFound, provider, sub)
	}
	return match, nil
}

// WithGlobalLock exposes the user-store lock so callers performing a
// read-modify-write across multiple records (e.g. OAuth upsert checking
// FindUserByOAuthSubject before WriteUser) can serialise correctly.
func (u *UserStore) WithGlobalLock(ctx context.Context, fn func() error) error {
	return u.withGlobalLock(ctx, fn)
}
