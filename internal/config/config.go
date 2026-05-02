// Package config loads the layered tickets_please configuration:
// built-in defaults → ~/.tickets_please/config.yaml (optional) → environment.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config is the resolved configuration after layering defaults, file, and env.
type Config struct {
	DataDir                string `koanf:"data_dir"`
	AutoCommit             bool   `koanf:"auto_commit"`
	EmbedProvider          string `koanf:"embed_provider"`
	OllamaURL              string `koanf:"ollama_url"`
	OllamaModel            string `koanf:"ollama_model"`
	OpenAIKey              string `koanf:"openai_api_key"`
	MCPAgentKey            string `koanf:"mcp_agent_key"`
	MCPAgentName           string `koanf:"mcp_agent_name"`
	AgentSessionTTLMinutes int    `koanf:"agent_session_ttl_minutes"`
	AgentSessionMaxMinutes int    `koanf:"agent_session_max_minutes"`
	ProjectIdleMinutes     int    `koanf:"project_idle_minutes"`
	MaxLoadedProjects      int    `koanf:"max_loaded_projects"`
	LockTimeoutSeconds     int    `koanf:"lock_timeout_seconds"`
	FsnotifyEnabled        bool   `koanf:"fsnotify_enabled"`
	EnforceDependencies    bool   `koanf:"enforce_dependencies"`

	// Source describes where the config came from for logging.
	// Either "defaults" or the absolute path to the loaded yaml file.
	Source string `koanf:"-"`
}

// Defaults mirrors examples/config.yaml. Keep them in lockstep.
var defaults = map[string]any{
	"data_dir":                  "./.tickets_please",
	"auto_commit":               true,
	"embed_provider":            "ollama",
	"ollama_url":                "http://localhost:11434",
	"ollama_model":              "nomic-embed-text",
	"openai_api_key":            "",
	"mcp_agent_key":             "", // empty = generated at startup
	"mcp_agent_name":            "tickets_please_mcp",
	"agent_session_ttl_minutes": 60,
	"agent_session_max_minutes": 240,
	"project_idle_minutes":      15,
	"max_loaded_projects":       16,
	"lock_timeout_seconds":      10,
	"fsnotify_enabled":          true,
	"enforce_dependencies":      false,
}

// configPath returns the absolute path of the per-user config file.
func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".tickets_please", "config.yaml"), nil
}

// Load builds a Config from layered providers: defaults → file → env.
// Missing config file is not an error.
func Load() (Config, error) {
	k := koanf.New(".")

	if err := k.Load(confmap.Provider(defaults, "."), nil); err != nil {
		return Config{}, fmt.Errorf("load defaults: %w", err)
	}

	source := "defaults"
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}
	if _, statErr := os.Stat(path); statErr == nil {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return Config{}, fmt.Errorf("load %s: %w", path, err)
		}
		source = path
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("stat %s: %w", path, statErr)
	}

	// Env: only consider env vars whose lowercased name matches one of our flat keys
	// (data_dir, auto_commit, ollama_url, …). koanf's env provider splits on its
	// delimiter to produce hierarchical paths; we use "." (no env vars contain a dot)
	// so the lowercased name is taken verbatim and matches our flat config keys.
	allowed := make(map[string]struct{}, len(defaults))
	for key := range defaults {
		allowed[key] = struct{}{}
	}
	envCb := func(s string) string {
		lower := strings.ToLower(s)
		if _, ok := allowed[lower]; !ok {
			return ""
		}
		return lower
	}
	if err := k.Load(env.Provider("", ".", envCb), nil); err != nil {
		return Config{}, fmt.Errorf("load env: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal config: %w", err)
	}
	cfg.Source = source
	return cfg, nil
}
