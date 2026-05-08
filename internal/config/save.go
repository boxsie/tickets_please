package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SaveYAMLNode reads path, unmarshals into a *yaml.Node, runs modify on it,
// and atomically writes the marshalled result back to path. Comments and
// key order are preserved across the round-trip. If modify returns an
// error, the original file is left untouched.
func SaveYAMLNode(path string, modify func(*yaml.Node) error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if err := modify(&root); err != nil {
		return err
	}
	out, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(out); err != nil {
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

// SetScalar updates the scalar value under key in the top-level mapping of
// root, preserving the existing node's comments. If the key is missing, it
// is appended (no comment). root may be a DocumentNode wrapping a mapping,
// or a MappingNode directly.
func SetScalar(root *yaml.Node, key, value string) error {
	mapping, err := topMapping(root)
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		if k.Value == key {
			v := mapping.Content[i+1]
			if v.Kind != yaml.ScalarNode {
				return fmt.Errorf("config: key %q is not a scalar", key)
			}
			v.Value = value
			v.Tag = ""
			v.Style = 0
			return nil
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value},
	)
	return nil
}

func topMapping(root *yaml.Node) (*yaml.Node, error) {
	if root == nil {
		return nil, errors.New("config: nil root node")
	}
	n := root
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil, errors.New("config: empty document")
		}
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil, errors.New("config: root is not a mapping")
	}
	return n, nil
}
