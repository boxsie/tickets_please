// Single binary for tickets_please. Subcommands:
//
//	mcp   (default) — stdio MCP server. Stub until T12.
//	check          — integrity check + exit. Stub until T02.
//	init           — create the .tickets_please/ data dir scaffold.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"tickets_please/internal/config"
)

func main() {
	sub := "mcp"
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	logger.Info("tickets_please starting",
		"binary", "tickets_please",
		"subcommand", sub,
		"config_source", cfg.Source,
	)

	switch sub {
	case "mcp":
		logger.Info("mcp mode not implemented yet, see T12")
	case "check":
		logger.Info("integrity check not implemented yet, see T02")
	case "init":
		if err := runInit(logger, cfg); err != nil {
			logger.Error("init failed", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q (use one of: mcp, check, init)\n", sub)
		os.Exit(2)
	}
}

// runInit creates the data-dir scaffold under cfg.DataDir and writes a README
// describing the layout if one does not already exist.
func runInit(logger *slog.Logger, cfg config.Config) error {
	dataDir := cfg.DataDir
	if dataDir == "" {
		return errors.New("data_dir is empty")
	}

	subdirs := []string{"agents", "projects", ".staging"}
	for _, sd := range subdirs {
		full := filepath.Join(dataDir, sd)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", full, err)
		}
	}

	readme := filepath.Join(dataDir, "README.md")
	if _, err := os.Stat(readme); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(readme, []byte(dataDirReadme), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", readme, err)
		}
		logger.Info("wrote data dir README", "path", readme)
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", readme, err)
	}

	logger.Info("data dir ready", "data_dir", dataDir)
	return nil
}

const dataDirReadme = "# .tickets_please/ — the data directory\n\n" +
	"This directory is the on-disk store for everything tickets_please tracks.\n" +
	"It is intentionally human-readable and committed to git so the project's\n" +
	"ticket history travels with the repo.\n\n" +
	"- `agents/<session-uuid>.yaml` — one file per agent session (active or expired).\n" +
	"- `projects/<slug>/` — one dir per project. Contains `project.yaml`, `summary.md`,\n" +
	"  `summary.embedding.json`, and `tickets/` (plus optional `phases/`).\n" +
	"- `.staging/` — transient atomicity scratch dir; safe to delete when no server\n" +
	"  is running. Gitignored.\n\n" +
	"See `../SPEC.md` (Data layout) for the canonical schema.\n"
