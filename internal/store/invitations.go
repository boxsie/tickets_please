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

// dirInvitations is the subdirectory under the InvitationStore root holding
// `<project_id>/<id>.yaml` files — the same per-project layout as memberships,
// so a project delete is one `os.RemoveAll`.
const dirInvitations = "invitations"

// InvitationStore is the filesystem handle for the per-project pending-invite
// registry. Decoupled from per-repo *Store like the agent/user/membership
// stores, and shares the same central data root + global `.lock`.
//
// On-disk layout:
//
//	<Root>/invitations/<project-id>/<invitation-id>.yaml
//	<Root>/.lock
type InvitationStore struct {
	Root               string
	LockTimeoutSeconds int
}

// NewInvitationStore resolves root, creates `<root>/invitations/` and
// `<root>/.staging/`, and returns the handle.
func NewInvitationStore(root string, lockTimeoutSeconds int) (*InvitationStore, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve invitation store root: %w", err)
	}
	for _, sub := range []string{dirInvitations, dirStaging} {
		if err := os.MkdirAll(filepath.Join(abs, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir invitation store %s: %w", sub, err)
		}
	}
	lts := lockTimeoutSeconds
	if lts <= 0 {
		lts = 10
	}
	return &InvitationStore{Root: abs, LockTimeoutSeconds: lts}, nil
}

func (s *InvitationStore) invitationsDir() string {
	return filepath.Join(s.Root, dirInvitations)
}

func (s *InvitationStore) projectDir(projectID string) string {
	return filepath.Join(s.invitationsDir(), projectID)
}

func (s *InvitationStore) recordPath(projectID, id string) string {
	return filepath.Join(s.projectDir(projectID), id+".yaml")
}

func (s *InvitationStore) withGlobalLock(ctx context.Context, fn func() error) error {
	path := filepath.Join(s.Root, fileLock)
	f, err := acquireFlock(ctx, path, time.Duration(s.LockTimeoutSeconds)*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return fn()
}

// CreateInvitation writes a new invitation record. ID, ProjectID, Role, and
// Token are required.
func (s *InvitationStore) CreateInvitation(ctx context.Context, rec *InvitationRecord) (*InvitationRecord, error) {
	if rec.ID == "" || rec.ProjectID == "" || rec.Token == "" {
		return nil, fmt.Errorf("CreateInvitation: id, project_id and token required")
	}
	if !validRole(rec.Role) {
		return nil, fmt.Errorf("CreateInvitation: invalid role %q", rec.Role)
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	err := s.withGlobalLock(ctx, func() error {
		if err := os.MkdirAll(s.projectDir(rec.ProjectID), 0o755); err != nil {
			return fmt.Errorf("mkdir project invitations: %w", err)
		}
		return WriteYAMLAtomic(s.recordPath(rec.ProjectID, rec.ID), rec)
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// ListInvitationsForProject returns every pending invitation for a project,
// sorted by CreatedAt (then ID). Missing project dir is "no invitations".
func (s *InvitationStore) ListInvitationsForProject(projectID string) ([]*InvitationRecord, error) {
	if projectID == "" {
		return nil, fmt.Errorf("ListInvitationsForProject: empty project id")
	}
	entries, err := os.ReadDir(s.projectDir(projectID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read project invitations dir: %w", err)
	}
	out := make([]*InvitationRecord, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		rec := &InvitationRecord{}
		if err := ReadYAML(filepath.Join(s.projectDir(projectID), e.Name()), rec); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// FindByToken scans every project's invitations for a matching token. O(N) in
// total invitations — fine at homelab scale, and the accept route only has the
// opaque token to go on. Returns domain.ErrNotFound when no invite matches.
func (s *InvitationStore) FindByToken(token string) (*InvitationRecord, error) {
	if token == "" {
		return nil, fmt.Errorf("FindByToken: empty token")
	}
	projectDirs, err := os.ReadDir(s.invitationsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: invitation token", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("read invitations dir: %w", err)
	}
	for _, d := range projectDirs {
		if !d.IsDir() {
			continue
		}
		entries, err := os.ReadDir(s.projectDir(d.Name()))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			rec := &InvitationRecord{}
			if err := ReadYAML(filepath.Join(s.projectDir(d.Name()), e.Name()), rec); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return nil, err
			}
			if rec.Token == token {
				return rec, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: invitation token", domain.ErrNotFound)
}

// DeleteInvitation removes (project_id, id). Missing is not an error
// (idempotent). Used to consume an invite on accept and to cancel a pending one.
func (s *InvitationStore) DeleteInvitation(ctx context.Context, projectID, id string) error {
	if projectID == "" || id == "" {
		return fmt.Errorf("DeleteInvitation: project_id and id required")
	}
	return s.withGlobalLock(ctx, func() error {
		if err := os.Remove(s.recordPath(projectID, id)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("remove invitation: %w", err)
		}
		return nil
	})
}
