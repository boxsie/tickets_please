// Single binary for tickets_please. Subcommands:
//
//	mcp   (default) — stdio MCP server. Wraps svc.Service as 28 MCP tools.
//	check          — integrity check + exit. Stub until T02.
//	init           — create the .tickets_please/ data dir scaffold.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"tickets_please/internal/config"
	"tickets_please/internal/mcptools"
	"tickets_please/internal/svc"
)

// version is the MCP server version reported to clients. Bump when meaningful
// behaviour changes; semver-ish.
const version = "0.1.0"

// totalTools is the canonical tool count exposed by the MCP server. Mirrored
// in the SPEC.md "MCP server" section table; if you change this, update both.
const totalTools = 28

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
		if err := runMCP(cfg, logger); err != nil {
			logger.Error("mcp failed", "err", err)
			os.Exit(1)
		}
	case "check":
		logger.Info("integrity check not implemented yet, see T02")
	case "init":
		if err := runInit(logger, cfg); err != nil {
			logger.Error("init failed", "err", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q (use one of: mcp, check, init)\n", sub)
		os.Exit(2)
	}
}

// runMCP boots the in-process Service, self-registers as an agent, attaches
// every MCP tool to a fresh stdio server, and serves until stdin closes.
//
// signal.NotifyContext gives a graceful shutdown path on Ctrl-C / SIGTERM —
// the deferred svc.Close drains the embedding worker and releases watchers.
func runMCP(cfg config.Config, log *slog.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	s, err := svc.New(cfg)
	if err != nil {
		return fmt.Errorf("build service: %w", err)
	}
	defer s.Close()

	identity := mcptools.NewIdentity(cfg)
	if err := identity.Register(ctx, s); err != nil {
		return fmt.Errorf("register mcp agent: %w", err)
	}

	server := mcpserver.NewMCPServer(
		"tickets_please",
		version,
		mcpserver.WithInstructions(mcptools.ServerInstructions),
	)
	tools := mcptools.NewTools(s, &identity, log)
	tools.RegisterAll(server)

	log.Info("mcp server starting",
		"tools", totalTools,
		"agent_key", identity.Key,
		"agent_name", identity.Name,
		"session", identity.SessionID(),
	)
	if err := mcpserver.ServeStdio(server); err != nil {
		// ServeStdio returns nil on clean stdin close. Anything else is an
		// actual transport error — surface it so the caller exits non-zero.
		return fmt.Errorf("serve stdio: %w", err)
	}
	return nil
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

// printUsage writes a short subcommand summary to stdout.
func printUsage() {
	fmt.Println(`tickets_please — single-binary MCP server for an LLM-first ticketing system.

Usage:
  tickets_please [subcommand]

Subcommands:
  mcp     Run the stdio MCP server (default if omitted).
  check   Run an integrity walk over the data dir and exit.
  init    Create the .tickets_please/ data dir scaffold.
  help    Show this message.

See SPEC.md and README.md (Wiring up MCP) for setup details.`)
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
