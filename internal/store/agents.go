package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const (
	// dirAgents holds the per-session yaml files that are live or recently
	// expired. Defined here since it is only referenced in this file after
	// being removed from store.go.
	dirAgents = "agents"
	// dirAgentsArchive holds records that expired more than an archive grace
	// ago (see ArchiveExpired). They are kept rather than deleted because
	// tickets and comments name their authoring agent by id, so ReadAgent
	// falls back here and attribution keeps resolving.
	dirAgentsArchive = "agents-archive"
	// dirAgentKeys maps sha256(key) to the id of the newest agent registered
	// under that key, so RegisterAgent's uniqueness check is one read rather
	// than a walk over every record ever minted.
	dirAgentKeys = "agent-keys"

	// archiveBatchSize bounds how many renames ArchiveExpired does per hold of
	// the global lock, so a large backlog never starves registrations.
	archiveBatchSize = 256
)

// AgentStore is the filesystem handle for the central agent registry. It is
// intentionally decoupled from *Store (the per-repo project store) so a
// long-running server can share one AgentStore across many project Stores.
//
// On-disk layout (unchanged from the earlier embedded layout):
//
//	<Root>/agents/<session-uuid>.yaml
//	<Root>/agents-archive/<session-uuid>.yaml  — long-expired, read-only
//	<Root>/agent-keys/<sha256(key)>            — key → newest agent id
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
	for _, sub := range []string{dirAgents, dirAgentsArchive, dirAgentKeys, dirStaging} {
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

func (a *AgentStore) archiveDir() string {
	return filepath.Join(a.Root, dirAgentsArchive)
}

// keyPath returns the key-index file for key. Keys are hashed because they
// are caller-supplied free text ("claude:run-a", "web-ui:3a9f…").
func (a *AgentStore) keyPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(a.Root, dirAgentKeys, hex.EncodeToString(sum[:]))
}

// WalkAgents iterates the live `agents/*.yaml` in filename order, calling fn
// with each parsed AgentRecord. Archived records are not visited — use
// WalkAllAgents when a caller needs every id that ever existed.
func (a *AgentStore) WalkAgents(fn func(rec *AgentRecord) error) error {
	return walkAgentDir(a.agentsDir(), fn)
}

// WalkAllAgents visits the live records and then the archived ones.
func (a *AgentStore) WalkAllAgents(fn func(rec *AgentRecord) error) error {
	if err := walkAgentDir(a.agentsDir(), fn); err != nil {
		return err
	}
	return walkAgentDir(a.archiveDir(), fn)
}

func walkAgentDir(dir string, fn func(rec *AgentRecord) error) error {
	names, err := agentFileNames(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		rec := &AgentRecord{}
		if err := ReadYAML(filepath.Join(dir, name), rec); err != nil {
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

// agentFileNames lists the *.yaml names in dir, sorted. A missing dir is empty.
func agentFileNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agents dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// ReadAgent loads the agent record for id from agents/, falling back to
// agents-archive/. Returns domain.ErrNotFound when neither holds it.
func (a *AgentStore) ReadAgent(id string) (*AgentRecord, error) {
	for _, dir := range []string{a.agentsDir(), a.archiveDir()} {
		rec := &AgentRecord{}
		err := ReadYAML(filepath.Join(dir, id+".yaml"), rec)
		if err == nil {
			return rec, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w: agent %s", domain.ErrNotFound, id)
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
		// Active-key uniqueness check (under lock so no TOCTOU). The key
		// index names the newest holder; only that one can still be active.
		holder, err := a.activeHolder(rec.Key, time.Now())
		if err != nil {
			return err
		}
		if holder != nil && holder.ID != rec.ID {
			return fmt.Errorf("%w: agent key %q is held by an active session", domain.ErrAlreadyExists, rec.Key)
		}

		path := filepath.Join(a.agentsDir(), rec.ID+".yaml")
		if err := WriteYAMLAtomic(path, rec); err != nil {
			return err
		}
		return writeFileAtomic(a.keyPath(rec.Key), []byte(rec.ID))
	})
}

// activeHolder returns the unexpired live record the key index points at for
// key, or nil. Caller holds the global lock.
func (a *AgentStore) activeHolder(key string, now time.Time) (*AgentRecord, error) {
	id, err := a.readKeyIndex(key)
	if err != nil || id == "" {
		return nil, err
	}
	rec := &AgentRecord{}
	if err := ReadYAML(filepath.Join(a.agentsDir(), id+".yaml"), rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if rec.Key != key || !rec.ExpiresAt.After(now) {
		return nil, nil
	}
	return rec, nil
}

// readKeyIndex returns the agent id recorded for key, or "" when none is.
func (a *AgentStore) readKeyIndex(key string) (string, error) {
	data, err := os.ReadFile(a.keyPath(key))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read agent key index: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// ArchiveReport counts what one ArchiveExpired pass did.
type ArchiveReport struct {
	Archived int // records moved agents/ → agents-archive/
	Indexed  int // live records whose missing key-index entry was backfilled
}

// ArchiveExpired moves records that expired more than grace before now into
// agents-archive/, dropping their key-index entry when it still points at
// them, and backfills the key index for live records written before it
// existed. Records are read outside the lock (an expired record is never
// rewritten, and a live one's key never changes); the moves and index writes
// happen under it in batches of archiveBatchSize.
func (a *AgentStore) ArchiveExpired(ctx context.Context, now time.Time, grace time.Duration) (ArchiveReport, error) {
	var report ArchiveReport
	names, err := agentFileNames(a.agentsDir())
	if err != nil {
		return report, err
	}
	var toArchive, toIndex []*AgentRecord
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		rec := &AgentRecord{}
		if err := ReadYAML(filepath.Join(a.agentsDir(), name), rec); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return report, err
		}
		switch {
		case rec.ExpiresAt.Add(grace).Before(now):
			toArchive = append(toArchive, rec)
		case rec.ExpiresAt.After(now) && rec.Key != "":
			toIndex = append(toIndex, rec)
		}
	}

	for start := 0; start < len(toArchive); start += archiveBatchSize {
		batch := toArchive[start:min(start+archiveBatchSize, len(toArchive))]
		if err := a.withGlobalLock(ctx, func() error {
			for _, rec := range batch {
				moved, err := a.archiveOne(rec)
				if err != nil {
					return err
				}
				if moved {
					report.Archived++
				}
			}
			return nil
		}); err != nil {
			return report, err
		}
	}

	if len(toIndex) > 0 {
		if err := a.withGlobalLock(ctx, func() error {
			for _, rec := range toIndex {
				id, err := a.readKeyIndex(rec.Key)
				if err != nil {
					return err
				}
				if id != "" {
					continue
				}
				if err := writeFileAtomic(a.keyPath(rec.Key), []byte(rec.ID)); err != nil {
					return err
				}
				report.Indexed++
			}
			return nil
		}); err != nil {
			return report, err
		}
	}
	return report, nil
}

// archiveOne moves rec into the archive and clears its key-index entry if it
// is still the named holder. Caller holds the global lock.
func (a *AgentStore) archiveOne(rec *AgentRecord) (bool, error) {
	name := rec.ID + ".yaml"
	if err := os.Rename(filepath.Join(a.agentsDir(), name), filepath.Join(a.archiveDir(), name)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil // a sibling process archived it first
		}
		return false, fmt.Errorf("archive agent %s: %w", rec.ID, err)
	}
	if rec.Key == "" {
		return true, nil
	}
	id, err := a.readKeyIndex(rec.Key)
	if err != nil {
		return true, err
	}
	if id == rec.ID {
		if err := os.Remove(a.keyPath(rec.Key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return true, fmt.Errorf("drop agent key index: %w", err)
		}
	}
	return true, nil
}

// WriteAgentRecord overwrites an existing agent yaml in place atomically.
// Used by Heartbeat-style flows to bump LastSeenAt without going through a
// full RegisterAgent flow. Per the SPEC, single-file writes can use the
// temp-file+rename helper directly.
func (a *AgentStore) WriteAgentRecord(rec *AgentRecord) error {
	path := filepath.Join(a.agentsDir(), rec.ID+".yaml")
	return WriteYAMLAtomic(path, rec)
}
