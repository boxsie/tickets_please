// Package config loads the layered tickets_please configuration:
// built-in defaults → ~/.tickets_please/config.yaml (optional) → environment.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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
	// DataDir is the per-repo project data directory (default ./.tickets_please).
	// Project content (project.yaml, phases/, tickets/, etc.) lives here.
	DataDir string `koanf:"data_dir"`
	// DataRoot is the central data root shared across all repos managed by this
	// server instance (default ~/.tickets_please). Agent sessions live here at
	// <DataRoot>/agents/<uuid>.yaml. It is separate from DataDir so a
	// long-running server can serve multiple repos without each one having its
	// own copy of the agent registry.
	DataRoot string `koanf:"data_root"`
	// RemoteProjectRoot bounds where create_project may materialise a project
	// directory when the caller's project_path does not exist on the server.
	// Defaults to <DataRoot>/projects (tilde-expanded). A create_project call
	// whose project_path is missing AND falls outside this root is rejected;
	// existing paths are used as-is regardless of root. Empty disables the
	// "auto-create on missing path" behaviour, restoring the strict stdio
	// pre-HTTP semantics.
	RemoteProjectRoot      string `koanf:"remote_project_root"`
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

	// Auth holds the optional web-UI OAuth configuration (W2). When no
	// providers are configured the login page renders an "unconfigured" hint
	// and the rest of the web UI keeps working under the legacy
	// localhost-only agent-cookie model. File-only config — secrets don't
	// belong in env vars, so the env layer's allowlist deliberately omits it.
	Auth AuthConfig `koanf:"auth"`

	// Source describes where the config came from for logging.
	// Either "defaults" or the absolute path to the loaded yaml file.
	Source string `koanf:"-"`
}

// AuthConfig is the `auth:` block of config.yaml. BaseURL is used to construct
// the OAuth redirect URL (`<base_url>/auth/<provider>/callback`); when empty
// the web layer infers it from the request Host (dev mode). Providers maps a
// provider name ("github", "google") to its OAuth client credentials.
type AuthConfig struct {
	BaseURL   string                        `koanf:"base_url"`
	Providers map[string]AuthProviderConfig `koanf:"providers"`
}

// AuthProviderConfig is a single OAuth app's credentials.
type AuthProviderConfig struct {
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`
}

// Defaults mirrors examples/config.yaml. Keep them in lockstep.
var defaults = map[string]any{
	"data_dir":                  "./.tickets_please",
	"data_root":                 "~/.tickets_please",
	"remote_project_root":       "~/.tickets_please/projects",
	"auto_commit":               true,
	"embed_provider":            "ollama",
	"ollama_url":                "http://localhost:11434",
	"ollama_model":              "bge-m3",
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

// UserConfigPath is the exported variant of configPath, used by the web UI to
// locate the on-disk config file when applying settings updates. Returns the
// canonical ~/.tickets_please/config.yaml path even when the file does not
// (yet) exist — callers that need a guaranteed-existing file should fall back
// to writing the path themselves.
func UserConfigPath() (string, error) { return configPath() }

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

	// koanf does not tilde-expand paths. Expand DataRoot manually.
	cfg.DataRoot = expandTilde(cfg.DataRoot)
	cfg.RemoteProjectRoot = expandTilde(cfg.RemoteProjectRoot)

	return cfg, nil
}

// expandTilde replaces a leading "~/" with the user's home directory. If the
// home dir cannot be determined, falls back to "./.tickets_please-central" and
// logs a warning so callers always get a non-empty, usable value.
func expandTilde(p string) string {
	if !strings.HasPrefix(p, "~/") && p != "~" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("config: cannot determine home dir for data_root; using fallback",
			"fallback", "./.tickets_please-central", "err", err)
		return "./.tickets_please-central"
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
