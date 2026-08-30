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

// dirMemberships is the subdirectory under the MembershipStore root holding
// `<project_id>/<user_id>.yaml` files. Per-project subdir means deleting a
// project's memberships is one `os.RemoveAll`.
const dirMemberships = "memberships"

// MembershipStore is the filesystem handle for the per-project membership
// registry. Decoupled from per-repo *Store for the same reason AgentStore is.
//
// On-disk layout:
//
//	<Root>/memberships/<project-id>/<user-id>.yaml
//	<Root>/.lock
type MembershipStore struct {
	Root               string
	LockTimeoutSeconds int
}

// NewMembershipStore resolves root, creates `<root>/memberships/` and
// `<root>/.staging/`, returns the handle.
func NewMembershipStore(root string, lockTimeoutSeconds int) (*MembershipStore, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve membership store root: %w", err)
	}
	for _, sub := range []string{dirMemberships, dirStaging} {
		if err := os.MkdirAll(filepath.Join(abs, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir membership store %s: %w", sub, err)
		}
	}
	lts := lockTimeoutSeconds
	if lts <= 0 {
		lts = 10
	}
	return &MembershipStore{Root: abs, LockTimeoutSeconds: lts}, nil
}

func (m *MembershipStore) membershipsDir() string {
	return filepath.Join(m.Root, dirMemberships)
}

func (m *MembershipStore) projectDir(projectID string) string {
	return filepath.Join(m.membershipsDir(), projectID)
}

func (m *MembershipStore) recordPath(projectID, userID string) string {
	return filepath.Join(m.projectDir(projectID), userID+".yaml")
}

func (m *MembershipStore) withGlobalLock(ctx context.Context, fn func() error) error {
	path := filepath.Join(m.Root, fileLock)
	f, err := acquireFlock(ctx, path, time.Duration(m.LockTimeoutSeconds)*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return fn()
}

// ListMembershipsForUser scans every project directory and returns the
// memberships the given user holds. Sorted by ProjectID for determinism.
func (m *MembershipStore) ListMembershipsForUser(userID string) ([]*MembershipRecord, error) {
	if userID == "" {
		return nil, fmt.Errorf("ListMembershipsForUser: empty user id")
	}
	projectDirs, err := os.ReadDir(m.membershipsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read memberships dir: %w", err)
	}
	out := make([]*MembershipRecord, 0)
	for _, d := range projectDirs {
		if !d.IsDir() {
			continue
		}
		rec, err := m.readRecord(d.Name(), userID)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProjectID < out[j].ProjectID })
	return out, nil
}

// ListMembersOfProject returns every membership for a project, sorted by
// UserID. Missing project dir is not an error — it's just "no members yet".
func (m *MembershipStore) ListMembersOfProject(projectID string) ([]*MembershipRecord, error) {
	if projectID == "" {
		return nil, fmt.Errorf("ListMembersOfProject: empty project id")
	}
	entries, err := os.ReadDir(m.projectDir(projectID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read project memberships dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]*MembershipRecord, 0, len(names))
	for _, name := range names {
		rec := &MembershipRecord{}
		if err := ReadYAML(filepath.Join(m.projectDir(projectID), name), rec); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// GrantMembership writes (project_id, user_id) → role. Idempotent: a repeat
// call with the same role is a no-op (returns the existing record); a call
// with a different role overwrites and bumps GrantedAt + GrantedBy. Validates
// the role before writing.
func (m *MembershipStore) GrantMembership(ctx context.Context, rec *MembershipRecord) (*MembershipRecord, error) {
	if rec.UserID == "" || rec.ProjectID == "" {
		return nil, fmt.Errorf("GrantMembership: user_id and project_id required")
	}
	if !validRole(rec.Role) {
		return nil, fmt.Errorf("GrantMembership: invalid role %q", rec.Role)
	}
	var out *MembershipRecord
	err := m.withGlobalLock(ctx, func() error {
		existing, readErr := m.readRecord(rec.ProjectID, rec.UserID)
		if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
			return readErr
		}
		if existing != nil && existing.Role == rec.Role {
			out = existing
			return nil
		}
		if rec.GrantedAt.IsZero() {
			rec.GrantedAt = time.Now().UTC()
		}
		if err := os.MkdirAll(m.projectDir(rec.ProjectID), 0o755); err != nil {
			return fmt.Errorf("mkdir project memberships: %w", err)
		}
		if err := WriteYAMLAtomic(m.recordPath(rec.ProjectID, rec.UserID), rec); err != nil {
			return err
		}
		out = rec
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeMembership deletes (project_id, user_id). Missing is not an error
// (idempotent removal). Returns true if a record existed and was removed.
func (m *MembershipStore) RevokeMembership(ctx context.Context, projectID, userID string) (bool, error) {
	if projectID == "" || userID == "" {
		return false, fmt.Errorf("RevokeMembership: project_id and user_id required")
	}
	var removed bool
	err := m.withGlobalLock(ctx, func() error {
		path := m.recordPath(projectID, userID)
		if err := os.Remove(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("remove membership: %w", err)
		}
		removed = true
		return nil
	})
	return removed, err
}

// GetMembership returns the (project, user) membership record, or a
// domain.ErrNotFound-wrapped error when the user holds no membership on the
// project. Used by the web route guards to authorize per-project access.
func (m *MembershipStore) GetMembership(projectID, userID string) (*MembershipRecord, error) {
	if projectID == "" || userID == "" {
		return nil, fmt.Errorf("GetMembership: project_id and user_id required")
	}
	rec, err := m.readRecord(projectID, userID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: membership %s/%s", domain.ErrNotFound, projectID, userID)
		}
		return nil, err
	}
	return rec, nil
}

// readRecord is the unlocked single-record reader used by the grant flow's
// existence check. Callers needing safety against concurrent writes must hold
// the global lock around the read+write pair.
func (m *MembershipStore) readRecord(projectID, userID string) (*MembershipRecord, error) {
	rec := &MembershipRecord{}
	if err := ReadYAML(m.recordPath(projectID, userID), rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// WithGlobalLock exposes the membership-store lock to callers that need to
// serialise a multi-record sequence (e.g. role-change UI that reads-then-writes
// while displaying the current member list).
func (m *MembershipStore) WithGlobalLock(ctx context.Context, fn func() error) error {
	return m.withGlobalLock(ctx, fn)
}

func validRole(r domain.Role) bool {
	switch r {
	case domain.RoleOwner, domain.RoleMember, domain.RoleViewer:
		return true
	}
	return false
}
