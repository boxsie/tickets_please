package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"tickets_please/internal/config"
)

func TestRunInitWritesGitignore(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, ".tickets_please")
	dataRoot := filepath.Join(tmp, "central")

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := config.Config{DataDir: dataDir, DataRoot: dataRoot}

	if err := runInit(logger, cfg); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	gi := filepath.Join(dataDir, ".gitignore")
	got, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	want := "*.embedding.json\n.staging/\n"
	if string(got) != want {
		t.Fatalf("gitignore contents = %q, want %q", string(got), want)
	}
}

func TestRunInitDoesNotClobberEditedGitignore(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, ".tickets_please")
	dataRoot := filepath.Join(tmp, "central")

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := config.Config{DataDir: dataDir, DataRoot: dataRoot}

	if err := runInit(logger, cfg); err != nil {
		t.Fatalf("runInit (first): %v", err)
	}

	gi := filepath.Join(dataDir, ".gitignore")
	custom := "*.embedding.json\n.staging/\n# user edit\nfoo.tmp\n"
	if err := os.WriteFile(gi, []byte(custom), 0o644); err != nil {
		t.Fatalf("write custom gitignore: %v", err)
	}

	if err := runInit(logger, cfg); err != nil {
		t.Fatalf("runInit (second): %v", err)
	}

	got, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if string(got) != custom {
		t.Fatalf("gitignore was clobbered: got %q, want %q", string(got), custom)
	}
}
