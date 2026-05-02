package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"tickets_please/internal/domain"
)

// isInGitRepo returns true if path (or any ancestor) contains a `.git`
// directory or file. Cheap probe used at Store.New time so we can disable
// auto-commit when the data dir isn't tracked.
func isInGitRepo(path string) bool {
	cur := path
	for {
		gitPath := filepath.Join(cur, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			// Either a directory (regular repo) or a file (submodule).
			_ = info
			return true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}

// gitCommit stages the given absolute paths and produces a single commit
// authored as the given agent. Called from StageOp.Commit when AutoCommit is
// enabled and an agent identity is attached.
//
// On a non-git directory or with `gitDisabled` set, this is a no-op (the
// warn-log already fired at startup). Failures here are logged by the caller
// — they don't roll back the StageOp.
func (s *Store) gitCommit(ctx context.Context, paths []string, agent *domain.Agent, summary string) error {
	if s.gitDisabled || !s.AutoCommit {
		return nil
	}

	repo, repoRoot, err := openRepoFor(s.Root)
	if err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}

	for _, p := range paths {
		rel, err := filepath.Rel(repoRoot, p)
		if err != nil {
			return fmt.Errorf("rel path %s: %w", p, err)
		}
		// go-git wants forward slashes regardless of platform.
		if _, err := wt.Add(filepath.ToSlash(rel)); err != nil {
			return fmt.Errorf("add %s: %w", rel, err)
		}
	}

	when := time.Now()
	msg := summary
	if msg == "" {
		msg = "[tickets_please] update [" + agent.Name + "]"
	} else if !hasPrefix(msg, "[tickets_please]") {
		msg = "[tickets_please] " + msg + " [" + agent.Name + "]"
	}

	_, err = wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  agent.Name,
			Email: agent.Key + "@tickets_please",
			When:  when,
		},
	})
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	_ = ctx
	return nil
}

// openRepoFor walks up from path looking for a `.git` directory and opens the
// repo at that root. Returns the opened repo and the absolute root path.
func openRepoFor(path string) (*git.Repository, string, error) {
	cur := path
	for {
		gitPath := filepath.Join(cur, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			repo, err := git.PlainOpen(cur)
			if err != nil {
				return nil, "", fmt.Errorf("open git repo at %s: %w", cur, err)
			}
			return repo, cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil, "", errors.New("not a git repo")
		}
		cur = parent
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
