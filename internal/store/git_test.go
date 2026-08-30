package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

func TestAutoCommit_ProducesOneCommit(t *testing.T) {
	// Init a fresh git repo in a temp dir, then point data_dir at a
	// subdir of it.
	repoDir := t.TempDir()
	if _, err := git.PlainInit(repoDir, false); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(repoDir, ".tickets_please")
	cfg := config.Config{
		DataDir:            dataDir,
		AutoCommit:         true,
		LockTimeoutSeconds: 5,
		FsnotifyEnabled:    false,
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.gitDisabled {
		t.Fatal("expected git enabled inside repo")
	}

	agent := &domain.Agent{ID: "a1", Key: "k1", Name: "Alice"}
	op, err := s.BeginOp()
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Write("projects/foo/project.yaml", []byte("id: foo\n")); err != nil {
		t.Fatal(err)
	}
	if err := op.Write("projects/foo/summary.md", []byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := op.Commit(context.Background(), LockProject("foo"), agent, "create project foo"); err != nil {
		t.Fatal(err)
	}

	// Check exactly one commit, authored by "Alice".
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if commit.Author.Name != "Alice" {
		t.Errorf("author name = %q, want Alice", commit.Author.Name)
	}
	if commit.Author.Email != "k1@tickets_please" {
		t.Errorf("author email = %q", commit.Author.Email)
	}
	// No prior commit (parent count 0 means this is the only commit).
	if commit.NumParents() != 0 {
		t.Errorf("expected single commit, got %d parents", commit.NumParents())
	}

	_ = time.Now
}

func TestAutoCommit_NoGitRepoDisablesSilently(t *testing.T) {
	cfg := config.Config{
		DataDir:            t.TempDir(),
		AutoCommit:         true,
		LockTimeoutSeconds: 5,
		FsnotifyEnabled:    false,
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !s.gitDisabled {
		t.Fatal("expected gitDisabled when not in a git repo")
	}
	// StageOp commit still succeeds.
	agent := &domain.Agent{ID: "a", Key: "k", Name: "Alice"}
	op, _ := s.BeginOp()
	if err := op.Write("foo.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := op.Commit(context.Background(), LockGlobal, agent, "x"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "foo.txt")); err != nil {
		t.Fatal(err)
	}
}
