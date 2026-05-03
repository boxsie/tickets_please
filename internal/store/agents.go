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

// dirAgents is the subdirectory name under the AgentStore root that holds
// per-session yaml files. Defined here since it is only referenced in this
// file after being removed from store.go.
const dirAgents = "agents"

// AgentStore is the filesystem handle for the central agent registry. It is
// intentionally decoupled from *Store (the per-repo project store) so a
// long-running server can share one AgentStore across many project Stores.
//
// On-disk layout (unchanged from the earlier embedded layout):
//
//	<Root>/agents/<session-uuid>.yaml
//	<Root>/.lock            — exclusive flock for cross-process serialisation
//	<Root>/.staging/        — scratch dir (reserved; not used by current ops)
type AgentStore struct {
	Root               string
	LockTimeoutSeconds int
}

// NewAgentStore resolves root to an absolute path, creates
// <root>/agents/ and <root>/.staging/, and returns the AgentStore. Returns an
// error if the directories cannot be created.
func NewAgentStore(root string, lockTimeoutSeconds int) (*AgentStore, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve agent store root: %w", err)
	}
	for _, sub := range []string{dirAgents, dirStaging} {
		if err := os.MkdirAll(filepath.Join(abs, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir agent store %s: %w", sub, err)
		}
	}
	lts := lockTimeoutSeconds
	if lts <= 0 {
		lts = 10
	}
	return &AgentStore{Root: abs, LockTimeoutSeconds: lts}, nil
}

// agentsDir returns the absolute path to the agents/ directory.
func (a *AgentStore) agentsDir() string {
	return filepath.Join(a.Root, dirAgents)
}

// withGlobalLock acquires the exclusive flock at <Root>/.lock, runs fn, and
// releases the lock. Mirrors Store.WithGlobalLock semantics. The acquireFlock
// poll-loop is shared via the unexported helper in lock.go.
func (a *AgentStore) withGlobalLock(ctx context.Context, fn func() error) error {
	path := filepath.Join(a.Root, fileLock)
	f, err := acquireFlock(ctx, path, time.Duration(a.LockTimeoutSeconds)*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return fn()
}

// WalkAgents iterates `agents/*.yaml` in filename order, calling fn with each
// parsed AgentRecord.
func (a *AgentStore) WalkAgents(fn func(rec *AgentRecord) error) error {
	entries, err := os.ReadDir(a.agentsDir())
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
		if err := ReadYAML(filepath.Join(a.agentsDir(), name), rec); err != nil {
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
func (a *AgentStore) ReadAgent(id string) (*AgentRecord, error) {
	rec := &AgentRecord{}
	path := filepath.Join(a.agentsDir(), id+".yaml")
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
// The write is performed under the global flock so concurrent registrations
// from sibling MCP processes serialize correctly. Single-file writes use
// WriteYAMLAtomic directly — the StageOp dance is overkill for agent yamls.
func (a *AgentStore) RegisterAgent(ctx context.Context, rec *AgentRecord) error {
	if rec.ID == "" {
		return fmt.Errorf("RegisterAgent: empty id")
	}
	if rec.Key == "" {
		return fmt.Errorf("RegisterAgent: empty key")
	}

	return a.withGlobalLock(ctx, func() error {
		// Active-key uniqueness check (under lock so no TOCTOU).
		now := time.Now()
		var conflict *AgentRecord
		if err := a.WalkAgents(func(existing *AgentRecord) error {
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

		path := filepath.Join(a.agentsDir(), rec.ID+".yaml")
		return WriteYAMLAtomic(path, rec)
	})
}

// WriteAgentRecord overwrites an existing agent yaml in place atomically.
// Used by Heartbeat-style flows to bump LastSeenAt without going through a
// full RegisterAgent flow. Per the SPEC, single-file writes can use the
// temp-file+rename helper directly.
func (a *AgentStore) WriteAgentRecord(rec *AgentRecord) error {
	path := filepath.Join(a.agentsDir(), rec.ID+".yaml")
	return WriteYAMLAtomic(path, rec)
}
