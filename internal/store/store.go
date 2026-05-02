package store

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"tickets_please/internal/config"
)

// Subdirectory and file names used throughout the store package. Centralized
// here so callers don't sprinkle string literals.
const (
	dirAgents   = "agents"
	dirProjects = "projects"
	dirStaging  = ".staging"
	dirPhases   = "phases"
	dirTickets  = "tickets"
	dirComments = "comments"

	fileProject    = "project.yaml"
	filePhase      = "phase.yaml"
	fileTicket     = "ticket.yaml"
	fileSummary    = "summary.md"
	fileBody       = "body.md"
	fileCompletion = "completion.md"
	fileLock       = ".lock"
)

// Store is the rooted filesystem handle every higher layer hangs off of.
// Reads do not lock; mutations go through StageOp + per-project flock. The
// concurrency model is documented in SPEC.md §Concurrent access.
type Store struct {
	Root               string
	AutoCommit         bool
	LockTimeoutSeconds int
	FsnotifyEnabled    bool
	Logger             *slog.Logger

	// gitDisabled is set when AutoCommit was requested but the data dir is
	// not inside a git repository. Surfaced by Commit so the warn-log only
	// fires once at startup.
	gitDisabled bool
}

// New resolves cfg.DataDir to an absolute path, creates the standard
// subdirectories if missing, and returns a Store. It does NOT run the
// integrity check — callers (the `check` subcommand and `Service.New`) call
// Integrity explicitly.
func New(cfg config.Config) (*Store, error) {
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data_dir: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data_dir: %w", err)
	}
	for _, sub := range []string{dirAgents, dirProjects, dirStaging} {
		if err := os.MkdirAll(filepath.Join(abs, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}

	logger := slog.Default()
	s := &Store{
		Root:               abs,
		AutoCommit:         cfg.AutoCommit,
		LockTimeoutSeconds: cfg.LockTimeoutSeconds,
		FsnotifyEnabled:    cfg.FsnotifyEnabled,
		Logger:             logger,
	}
	if s.LockTimeoutSeconds <= 0 {
		s.LockTimeoutSeconds = 10
	}

	// Probe for a git repo containing the data dir. If absent, mark
	// auto-commit disabled and warn-log once. The Commit method becomes a
	// no-op for the rest of the process lifetime.
	if s.AutoCommit {
		if !isInGitRepo(abs) {
			s.Logger.Warn("auto_commit requested but data_dir is not inside a git repo; commits disabled", "data_dir", abs)
			s.gitDisabled = true
		}
	}

	return s, nil
}

// projectDir returns the absolute path to a project's directory.
func (s *Store) projectDir(slug string) string {
	return filepath.Join(s.Root, dirProjects, slug)
}

// stagingDir returns the absolute path to the .staging root.
func (s *Store) stagingDir() string {
	return filepath.Join(s.Root, dirStaging)
}

// agentsDir returns the absolute path to the agents/ directory.
func (s *Store) agentsDir() string {
	return filepath.Join(s.Root, dirAgents)
}

// projectsDir returns the absolute path to the projects/ directory.
func (s *Store) projectsDir() string {
	return filepath.Join(s.Root, dirProjects)
}
