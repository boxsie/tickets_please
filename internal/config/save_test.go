package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func copyFixture(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "config.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatalf("write fixture copy: %v", err)
	}
	return path
}

func TestSetScalarPreservesCommentsAndOrder(t *testing.T) {
	path := copyFixture(t)
	original, _ := os.ReadFile(path)

	err := SaveYAMLNode(path, func(root *yaml.Node) error {
		return SetScalar(root, "ollama_model", "bge-m3")
	})
	if err != nil {
		t.Fatalf("SaveYAMLNode: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	out := string(got)

	for _, want := range []string{
		"# tickets_please configuration",
		"# Top-of-file banner comment.",
		"# Where per-repo project content lives.",
		"# Where shared agent state lives (across repos).",
		"# --- embedding section ---",
		"# Embedding provider to use: ollama | openai",
		"# inline comment",
		"# trailing comment",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing comment %q in output:\n%s", want, out)
		}
	}

	if !strings.Contains(out, "ollama_model: bge-m3") {
		t.Errorf("expected updated value, got:\n%s", out)
	}
	if strings.Contains(out, "nomic-embed-text") {
		t.Errorf("old value still present:\n%s", out)
	}

	wantOrder := []string{"data_dir", "data_root", "embed_provider", "ollama_url", "ollama_model"}
	last := -1
	for _, k := range wantOrder {
		idx := strings.Index(out, k+":")
		if idx <= last {
			t.Errorf("key %q out of order; got:\n%s", k, out)
		}
		last = idx
	}

	// Byte-identity check: every line that does NOT contain the targeted
	// key should appear verbatim in the new output.
	for _, line := range strings.Split(string(original), "\n") {
		if strings.Contains(line, "ollama_model") {
			continue
		}
		if line == "" {
			continue
		}
		if !strings.Contains(out, line) {
			t.Errorf("line lost in round-trip: %q", line)
		}
	}
}

func TestSetScalarAddsMissingKey(t *testing.T) {
	path := copyFixture(t)
	err := SaveYAMLNode(path, func(root *yaml.Node) error {
		return SetScalar(root, "openai_api_key", "sk-test")
	})
	if err != nil {
		t.Fatalf("SaveYAMLNode: %v", err)
	}
	got, _ := os.ReadFile(path)
	out := string(got)
	if !strings.Contains(out, "openai_api_key: sk-test") {
		t.Errorf("new key not appended:\n%s", out)
	}
	// The new key should sit after every original key.
	newIdx := strings.Index(out, "openai_api_key:")
	for _, k := range []string{"data_dir", "data_root", "embed_provider", "ollama_url", "ollama_model"} {
		if strings.Index(out, k+":") > newIdx {
			t.Errorf("new key should be appended after %q", k)
		}
	}
}

func TestSaveYAMLNodeAtomicOnModifyError(t *testing.T) {
	path := copyFixture(t)
	original, _ := os.ReadFile(path)

	sentinel := errors.New("boom")
	err := SaveYAMLNode(path, func(root *yaml.Node) error {
		// Mutate then bail out; the file on disk must remain untouched.
		_ = SetScalar(root, "ollama_model", "should-not-land")
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Errorf("file modified despite modify error\nbefore:\n%s\nafter:\n%s", original, after)
	}

	// And no stray temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}
