package store

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ReadYAML decodes a yaml file into v.
func ReadYAML(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// WriteYAMLAtomic writes v as yaml to path atomically (temp file + rename in
// the same directory). Used for in-place single-file writes that don't go
// through StageOp (e.g. an agent heartbeat updating last_seen_at). Multi-file
// mutations should use StageOp instead.
func WriteYAMLAtomic(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	return writeFileAtomic(path, data)
}

// writeFileAtomic writes data to path via a temp file in the same directory
// and an os.Rename. The dir's existence is the caller's responsibility.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// best-effort cleanup if rename failed
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// MarshalYAML returns v encoded as yaml bytes — a convenience for callers
// building up StageOp Write payloads.
func MarshalYAML(v any) ([]byte, error) {
	return yaml.Marshal(v)
}
