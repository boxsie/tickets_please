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

// WalkAgents iterates `agents/*.yaml` in filename order, calling fn with each
// parsed AgentRecord.
func (s *Store) WalkAgents(fn func(rec *AgentRecord) error) error {
	entries, err := os.ReadDir(s.agentsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read agents dir: %w", err)
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
		rec := &AgentRecord{}
		if err := ReadYAML(filepath.Join(s.agentsDir(), name), rec); err != nil {
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

// ReadAgent loads the agent record at agents/<id>.yaml. Returns
// domain.ErrNotFound when no such file exists.
func (s *Store) ReadAgent(id string) (*AgentRecord, error) {
	rec := &AgentRecord{}
	path := filepath.Join(s.agentsDir(), id+".yaml")
	if err := ReadYAML(path, rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: agent %s", domain.ErrNotFound, id)
		}
		return nil, err
	}
	return rec, nil
}

// RegisterAgent writes a new agent yaml at agents/<id>.yaml after verifying
// no other still-active agent record holds the same Key. Returns
// domain.ErrAlreadyExists on a Key collision with a non-expired record;
// callers can re-attempt registration once the existing session expires.
//
// The write goes through a StageOp under the global flock so concurrent
// registrations from sibling MCP processes serialize correctly.
func (s *Store) RegisterAgent(ctx context.Context, rec *AgentRecord) error {
	if rec.ID == "" {
		return fmt.Errorf("RegisterAgent: empty id")
	}
	if rec.Key == "" {
		return fmt.Errorf("RegisterAgent: empty key")
	}

	// Active-key uniqueness check.
	now := time.Now()
	var conflict *AgentRecord
	if err := s.WalkAgents(func(existing *AgentRecord) error {
		if existing.ID == rec.ID {
			return nil
		}
		if existing.Key == rec.Key && existing.ExpiresAt.After(now) {
			conflict = existing
		}
		return nil
	}); err != nil {
		return err
	}
	if conflict != nil {
		return fmt.Errorf("%w: agent key %q is held by an active session", domain.ErrAlreadyExists, rec.Key)
	}

	data, err := MarshalYAML(rec)
	if err != nil {
		return err
	}
	op, err := s.BeginOp()
	if err != nil {
		return err
	}
	defer op.Abort()
	rel := filepath.Join(dirAgents, rec.ID+".yaml")
	if err := op.Write(rel, data); err != nil {
		return err
	}
	return op.Commit(ctx, LockGlobal, nil, "")
}

// WriteAgentRecord overwrites an existing agent yaml in place atomically.
// Used by Heartbeat-style flows to bump LastSeenAt without going through
// StageOp. Per the SPEC, single-file writes can use the temp-file+rename
// helper directly.
func (s *Store) WriteAgentRecord(rec *AgentRecord) error {
	path := filepath.Join(s.agentsDir(), rec.ID+".yaml")
	return WriteYAMLAtomic(path, rec)
}
