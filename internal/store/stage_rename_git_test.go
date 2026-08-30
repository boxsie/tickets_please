package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// storeInFreshRepo builds a Store with auto-commit enabled inside a brand-new
// git repo, and returns it alongside the repo dir.
func storeInFreshRepo(t *testing.T) (*Store, string) {
	t.Helper()
	repoDir := t.TempDir()
	if _, err := git.PlainInit(repoDir, false); err != nil {
		t.Fatalf("git init: %v", err)
	}
	s, err := New(config.Config{
		DataDir:            filepath.Join(repoDir, ".tickets_please"),
		AutoCommit:         true,
		LockTimeoutSeconds: 5,
		FsnotifyEnabled:    false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.gitDisabled {
		t.Fatal("expected git enabled inside repo")
	}
	return s, repoDir
}

// headPaths lists every path in the repo's HEAD tree.
func headPaths(t *testing.T, repoDir string) []string {
	t.Helper()
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("commit object: %v", err)
	}
	files, err := commit.Files()
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	var paths []string
	if err := files.ForEach(func(f *object.File) error {
		paths = append(paths, f.Name)
		return nil
	}); err != nil {
		t.Fatalf("walk files: %v", err)
	}
	return paths
}

// TestRenameDir_AutoCommitStagesBothEnds is the regression pin for the bug
// where a staged rename recorded only its destination: the moved tree stayed
// present at its old path in the committed tree, so every clone saw the same
// ticket in two places.
func TestRenameDir_AutoCommitStagesBothEnds(t *testing.T) {
	s, repoDir := storeInFreshRepo(t)
	agent := &domain.Agent{ID: "a1", Key: "k1", Name: "Mover"}
	ctx := context.Background()

	// Commit a ticket dir at its original (phased) location.
	from := filepath.Join("phases", "001-alpha", "tickets", "007-widget")
	op, err := s.BeginOp()
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Write(filepath.Join(from, "ticket.yaml"), []byte("id: t7\nnumber: 7\n")); err != nil {
		t.Fatal(err)
	}
	if err := op.Write(filepath.Join(from, "body.md"), []byte("the widget\n")); err != nil {
		t.Fatal(err)
	}
	if err := op.Commit(ctx, LockProject("p"), agent, "create ticket 7"); err != nil {
		t.Fatal(err)
	}

	// Move it to the phase-less area, the way AssignTicketToPhase does.
	to := filepath.Join("tickets", "007-widget")
	op2, err := s.BeginOp()
	if err != nil {
		t.Fatal(err)
	}
	if err := op2.RenameDir(from, to); err != nil {
		t.Fatal(err)
	}
	if err := op2.Write(filepath.Join(to, "ticket.yaml"), []byte("id: t7\nnumber: 7\nphase_id: null\n")); err != nil {
		t.Fatal(err)
	}
	if err := op2.Commit(ctx, LockProject("p"), agent, "reassign ticket 7 to phase none"); err != nil {
		t.Fatal(err)
	}

	// On disk the move happened.
	if _, err := os.Stat(filepath.Join(s.Root, from)); !os.IsNotExist(err) {
		t.Errorf("source dir still on disk: %v", err)
	}

	// In the committed tree it must ALSO have happened. This is the assertion
	// the bug cannot pass: the old path lingered in HEAD.
	paths := headPaths(t, repoDir)
	var atOld, atNew int
	for _, p := range paths {
		switch {
		case filepath.Base(p) != "ticket.yaml":
		case containsDir(p, "phases"):
			atOld++
		default:
			atNew++
		}
	}
	if atOld != 0 {
		t.Errorf("ticket.yaml still present under phases/ in HEAD (%d); the rename's source was never staged.\nHEAD tree: %v", atOld, paths)
	}
	if atNew != 1 {
		t.Errorf("want exactly 1 ticket.yaml at the new path in HEAD, got %d.\nHEAD tree: %v", atNew, paths)
	}

	// And the worktree is clean — no dangling deletion for a human to notice.
	assertWorktreeClean(t, repoDir)
}

// TestRemoveAndWrite_StillStageCorrectly guards the other two op types against
// the interface widening that fixed the rename case.
func TestRemoveAndWrite_StillStageCorrectly(t *testing.T) {
	s, repoDir := storeInFreshRepo(t)
	agent := &domain.Agent{ID: "a1", Key: "k1", Name: "Mover"}
	ctx := context.Background()

	rel := filepath.Join("tickets", "008-doomed")
	op, err := s.BeginOp()
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Write(filepath.Join(rel, "ticket.yaml"), []byte("id: t8\n")); err != nil {
		t.Fatal(err)
	}
	if err := op.Commit(ctx, LockProject("p"), agent, "create ticket 8"); err != nil {
		t.Fatal(err)
	}
	assertWorktreeClean(t, repoDir)

	op2, err := s.BeginOp()
	if err != nil {
		t.Fatal(err)
	}
	if err := op2.RemovePath(rel); err != nil {
		t.Fatal(err)
	}
	if err := op2.Commit(ctx, LockProject("p"), agent, "delete ticket 8"); err != nil {
		t.Fatal(err)
	}

	for _, p := range headPaths(t, repoDir) {
		if containsDir(p, "008-doomed") {
			t.Errorf("deleted ticket still in HEAD: %s", p)
		}
	}
	assertWorktreeClean(t, repoDir)
}

// containsDir reports whether any path segment equals dir. go-git yields
// forward-slash paths, so split on "/" rather than the OS separator.
func containsDir(path, dir string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == dir {
			return true
		}
	}
	return false
}

func assertWorktreeClean(t *testing.T, repoDir string) {
	t.Helper()
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	st, err := wt.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	// The advisory flock file is untracked runtime state (a real repo
	// gitignores it); it isn't what this assertion is about.
	for path, fs := range st {
		if filepath.Base(path) == ".lock" {
			continue
		}
		t.Errorf("worktree not clean after commit: %s (staging=%q worktree=%q)",
			path, fs.Staging, fs.Worktree)
	}
}
