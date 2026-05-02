package vecindex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ReadSidecar parses an *.embedding.json file into a []float32. The file is
// a flat JSON array — no metadata, no length header.
func ReadSidecar(path string) ([]float32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var vec []float32
	if err := json.Unmarshal(data, &vec); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return vec, nil
}

// WriteSidecar serializes vec as JSON and writes it atomically to path —
// temp file in the same directory followed by os.Rename. Parent directories
// are created with 0o755 if missing.
func WriteSidecar(path string, vec []float32) error {
	data, err := json.Marshal(vec)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".embed-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// best-effort cleanup if the rename never happens
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
