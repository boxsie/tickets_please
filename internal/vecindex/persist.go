package vecindex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Sidecar is the on-disk schema for `*.embedding.json` files. It pairs the
// vector with the embedder identity (provider+model) and the dim (which is
// always len(Vec) but persisted explicitly so a quick metadata-only read
// can answer "wrong embedder?" without parsing the float array).
//
// Sidecars are gitignored, disposable, and freshly produced by a re-embed —
// there is intentionally no back-compat reader for the older flat-array
// shape. A cold clone rebuilds them from source.
type Sidecar struct {
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Dim      int       `json:"dim"`
	Vec      []float32 `json:"vec"`
}

// ReadSidecar parses an *.embedding.json file into a Sidecar.
func ReadSidecar(path string) (Sidecar, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Sidecar{}, err
	}
	var sc Sidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return Sidecar{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return sc, nil
}

// WriteSidecar serializes sc as JSON and writes it atomically to path —
// temp file in the same directory followed by os.Rename. Parent directories
// are created with 0o755 if missing.
func WriteSidecar(path string, sc Sidecar) error {
	data, err := json.Marshal(sc)
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
