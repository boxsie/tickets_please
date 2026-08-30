package svc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// CreateProjectAt with an existing local directory — the stdio-style case —
// uses the path as-is and writes .tickets_please/ inside.
func TestCreateProjectAt_ExistingPath_UsedAsIs(t *testing.T) {
	repoDir := t.TempDir()
	s := freshServiceWithCfg(t, config.Config{
		DataDir:           filepath.Join(t.TempDir(), ".tickets_please"),
		RemoteProjectRoot: "", // irrelevant — path already exists
	})

	p, err := s.CreateProjectAt(context.Background(), repoDir, "alpha", "Alpha", "", validSummary())
	if err != nil {
		t.Fatalf("CreateProjectAt existing path: %v", err)
	}
	if p.Slug != "alpha" {
		t.Fatalf("slug: got %q want alpha", p.Slug)
	}

	want := filepath.Join(repoDir, ".tickets_please", "project.yaml")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("project.yaml not written at %s: %v", want, err)
	}
}

// CreateProjectAt with a missing path and an empty RemoteProjectRoot
// (auto-create disabled) preserves the strict pre-HTTP behaviour: reject.
func TestCreateProjectAt_MissingPath_NoRoot_Rejected(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{
		RemoteProjectRoot: "",
	})

	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	_, err := s.CreateProjectAt(context.Background(), missing, "alpha", "Alpha", "", validSummary())
	if err == nil {
		t.Fatalf("expected error for missing path with no root; got nil")
	}
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("path should not have been created: %v", statErr)
	}
}

// CreateProjectAt with a missing path that falls under RemoteProjectRoot
// materialises the directory and writes the project. The supplied path
// becomes the stable identifier for subsequent register_agent calls.
func TestCreateProjectAt_MissingPath_UnderRoot_Materialised(t *testing.T) {
	root := t.TempDir()
	s := freshServiceWithCfg(t, config.Config{
		DataDir:           filepath.Join(t.TempDir(), ".tickets_please"),
		RemoteProjectRoot: root,
	})

	repoPath := filepath.Join(root, "ensemble")
	p, err := s.CreateProjectAt(context.Background(), repoPath, "ensemble", "Ensemble", "", validSummary())
	if err != nil {
		t.Fatalf("CreateProjectAt under root: %v", err)
	}
	if p.Slug != "ensemble" {
		t.Fatalf("slug: got %q want ensemble", p.Slug)
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".tickets_please", "project.yaml")); err != nil {
		t.Fatalf("project.yaml not materialised: %v", err)
	}
}

// CreateProjectAt with a missing path outside RemoteProjectRoot is rejected
// with a message pointing operators at the flag, even though the root is
// configured.
func TestCreateProjectAt_MissingPath_OutsideRoot_Rejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	s := freshServiceWithCfg(t, config.Config{
		RemoteProjectRoot: root,
	})

	missing := filepath.Join(outside, "ensemble")
	_, err := s.CreateProjectAt(context.Background(), missing, "ensemble", "Ensemble", "", validSummary())
	if err == nil {
		t.Fatalf("expected error for path outside root; got nil")
	}
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
	if !strings.Contains(err.Error(), "remote_project_root") {
		t.Fatalf("error should reference remote_project_root: %v", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("path should not have been created: %v", statErr)
	}
}

// Sanity: pathUnderRoot treats relative inputs as not-under and handles
// trailing separators + exact-match.
func TestPathUnderRoot(t *testing.T) {
	cases := []struct {
		name string
		p    string
		root string
		want bool
	}{
		{"exact match", "/a/b", "/a/b", true},
		{"child", "/a/b/c", "/a/b", true},
		{"nested", "/a/b/c/d", "/a/b", true},
		{"trailing sep on root", "/a/b/c", "/a/b/", true},
		{"sibling prefix", "/a/bc", "/a/b", false},
		{"unrelated", "/x/y", "/a/b", false},
		{"relative p", "a/b/c", "/a/b", false},
		{"relative root", "/a/b/c", "a/b", false},
		{"empty root", "/a/b", "", false},
	}
	for _, tc := range cases {
		if got := pathUnderRoot(tc.p, tc.root); got != tc.want {
			t.Errorf("%s: pathUnderRoot(%q,%q) = %v, want %v", tc.name, tc.p, tc.root, got, tc.want)
		}
	}
}
