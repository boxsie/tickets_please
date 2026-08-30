package config

import (
	"testing"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
)

// TestAuthConfigUnmarshal locks in the koanf struct-tag wiring for the nested
// auth block — the one non-flat part of Config, where a typo'd koanf tag would
// silently drop OAuth credentials.
func TestAuthConfigUnmarshal(t *testing.T) {
	k := koanf.New(".")
	src := map[string]any{
		"auth": map[string]any{
			"base_url":              "https://tickets.example.com",
			"session_max_age_hours": 24,
			"providers": map[string]any{
				"github": map[string]any{"client_id": "gh-id", "client_secret": "gh-secret"},
				"google": map[string]any{"client_id": "g-id", "client_secret": "g-secret"},
			},
		},
	}
	if err := k.Load(confmap.Provider(src, "."), nil); err != nil {
		t.Fatalf("load: %v", err)
	}
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Auth.BaseURL != "https://tickets.example.com" {
		t.Errorf("base_url = %q", cfg.Auth.BaseURL)
	}
	if cfg.Auth.SessionMaxAgeHours != 24 {
		t.Errorf("session_max_age_hours = %d, want 24", cfg.Auth.SessionMaxAgeHours)
	}
	gh, ok := cfg.Auth.Providers["github"]
	if !ok || gh.ClientID != "gh-id" || gh.ClientSecret != "gh-secret" {
		t.Errorf("github provider = %+v (ok=%v)", gh, ok)
	}
	g, ok := cfg.Auth.Providers["google"]
	if !ok || g.ClientID != "g-id" || g.ClientSecret != "g-secret" {
		t.Errorf("google provider = %+v (ok=%v)", g, ok)
	}
}
