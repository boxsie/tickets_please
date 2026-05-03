// Single binary for tickets_please. Subcommands:
//
//	mcp     (default) — stdio MCP server. Wraps svc.Service as 28 MCP tools.
//	check            — integrity check + exit. Stub until T02.
//	init             — create the .tickets_please/ data dir scaffold.
//	migrate <repo>   — flatten a v0.1 layout (.tickets_please/projects/<slug>/*)
//	                  to the v0.2 single-project shape (.tickets_please/*).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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
const version = "0.2.0"

// totalTools is the canonical tool count exposed by the MCP server. Mirrored
// in the SPEC.md "MCP server" section table; if you change this, update both.
const totalTools = 28

func main() {
	sub := "mcp"
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	// `migrate` is a tooling subcommand that runs entirely off-config (it
	// operates on a path the user passes), so handle it before config.Load.
	if sub == "migrate" {
		if err := runMigrate(os.Args[2:], logger); err != nil {
			logger.Error("migrate failed", "err", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		os.Exit(1)
	}

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
		fmt.Fprintf(os.Stderr, "unknown subcommand %q (use one of: mcp, check, init, migrate)\n", sub)
		os.Exit(2)
	}
}

// runMCP boots the in-process Service, pre-registers a stdio agent session,
// attaches every MCP tool to a fresh stdio server, and serves until stdin
// closes.
//
// The Registry is built first; then a single "stdio" session is synthesised
// from cfg defaults and registered under the fixed key "stdio" — which is
// also what mcp-go's stdioSession.SessionID() returns. This gives stdio
// clients (including CI/tests) transparent attribution without any extra
// handshake.
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

	// Build the registry and the default stdio session prototype.
	registry := mcptools.NewRegistry(cfg)
	sess := mcptools.DefaultStdioSession(cfg)

	// Register the agent in the svc layer to get a real agent session ID.
	agentID, expiresAt, err := s.RegisterAgent(ctx, sess.AgentKey, sess.AgentName, sess.Metadata, 0)
	if err != nil {
		return fmt.Errorf("register mcp agent: %w", err)
	}
	sess.AgentID = agentID
	sess.ExpiresAt = expiresAt

	// Pre-register under the synthetic "stdio" session ID so every tool call
	// via stdio transport finds its session without a register_agent round-trip.
	if err := registry.Register("stdio", sess); err != nil {
		return fmt.Errorf("pre-register stdio session: %w", err)
	}

	server := mcpserver.NewMCPServer(
		"tickets_please",
		version,
		mcpserver.WithInstructions(mcptools.ServerInstructions),
	)
	tools := mcptools.NewTools(s, registry, log)
	tools.RegisterAll(server)

	log.Info("mcp server starting",
		"tools", totalTools,
		"agent_key", sess.AgentKey,
		"agent_name", sess.AgentName,
		"agent_id", sess.AgentID,
	)
	if err := mcpserver.ServeStdio(server); err != nil {
		// ServeStdio returns nil on clean stdin close. Anything else is an
		// actual transport error — surface it so the caller exits non-zero.
		return fmt.Errorf("serve stdio: %w", err)
	}
	return nil
}

// runInit creates the data-dir scaffold under cfg.DataDir and writes a README
// describing the layout if one does not already exist. Post-flatten the
// scaffold is just `agents/` + `.staging/` — actual project content lands
// directly at the data-dir root once a project is created.
func runInit(logger *slog.Logger, cfg config.Config) error {
	dataDir := cfg.DataDir
	if dataDir == "" {
		return errors.New("data_dir is empty")
	}

	subdirs := []string{"agents", ".staging"}
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

// runMigrate flattens a v0.1 layout where project content lived under
// `.tickets_please/projects/<slug>/*` into the v0.2 shape where a project's
// files are siblings of `.tickets_please/agents/`. The migrate is idempotent:
// running it on an already-flat repo is a no-op.
//
// One repo holds at most one project (post-flatten), so a multi-project legacy
// layout aborts with an error pointing the operator at the conflict.
func runMigrate(args []string, log *slog.Logger) error {
	dryRun := false
	var repoPath string
	for _, a := range args {
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "-h" || a == "--help":
			fmt.Println("usage: tickets_please migrate <repo-path> [--dry-run]")
			return nil
		default:
			if repoPath != "" {
				return fmt.Errorf("unexpected extra argument %q (only one repo path allowed)", a)
			}
			repoPath = a
		}
	}
	if repoPath == "" {
		return errors.New("repo path required: tickets_please migrate <repo-path> [--dry-run]")
	}

	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("resolve repo path: %w", err)
	}

	dataDir := filepath.Join(abs, ".tickets_please")
	if info, err := os.Stat(dataDir); err != nil {
		return fmt.Errorf("stat %s: %w", dataDir, err)
	} else if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dataDir)
	}

	projectsDir := filepath.Join(dataDir, "projects")
	info, err := os.Stat(projectsDir)
	if errors.Is(err, os.ErrNotExist) {
		log.Info("migrate: already flat (no projects/ subdir)", "data_dir", dataDir)
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", projectsDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists but is not a directory", projectsDir)
	}

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", projectsDir, err)
	}
	slugDirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == "" || e.Name()[0] == '.' {
			continue
		}
		slugDirs = append(slugDirs, e.Name())
	}

	switch len(slugDirs) {
	case 0:
		log.Info("migrate: projects/ exists but is empty; removing it", "dir", projectsDir, "dry_run", dryRun)
		if !dryRun {
			if err := os.Remove(projectsDir); err != nil {
				return fmt.Errorf("remove empty projects dir: %w", err)
			}
		}
		return nil
	case 1:
		// Hoist contents below.
	default:
		return fmt.Errorf(
			"migrate: %s has %d project subdirs (%v); the new layout is one project per repo — split or pick one before migrating",
			projectsDir, len(slugDirs), slugDirs,
		)
	}

	slug := slugDirs[0]
	src := filepath.Join(projectsDir, slug)
	srcEntries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}

	// `.lock` is a runtime artefact (per-project flock) the legacy layout
	// kept inside the slug dir. The new layout has its own root-level lock,
	// so just drop the legacy one rather than try to hoist it.
	skip := map[string]bool{".lock": true}

	// Collision check: any non-skipped source name that already exists
	// under the data dir aborts the migration. Catches the edge case where
	// an operator ran a partial flatten by hand.
	for _, e := range srcEntries {
		if skip[e.Name()] {
			continue
		}
		dst := filepath.Join(dataDir, e.Name())
		if _, statErr := os.Stat(dst); statErr == nil {
			return fmt.Errorf("migrate: would overwrite %s — flatten appears partially applied; manual review required", dst)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", dst, statErr)
		}
	}

	log.Info("migrate: hoisting project to flat layout",
		"slug", slug,
		"from", src,
		"to", dataDir,
		"entries", len(srcEntries),
		"dry_run", dryRun,
	)
	if dryRun {
		for _, e := range srcEntries {
			if skip[e.Name()] {
				log.Info("migrate: would skip (runtime file)", "name", e.Name())
				continue
			}
			log.Info("migrate: would move", "name", e.Name())
		}
		return nil
	}

	for _, e := range srcEntries {
		from := filepath.Join(src, e.Name())
		if skip[e.Name()] {
			if err := os.Remove(from); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Warn("migrate: failed to drop legacy runtime file", "path", from, "err", err)
			}
			continue
		}
		to := filepath.Join(dataDir, e.Name())
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", from, to, err)
		}
	}
	if err := os.Remove(src); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warn("migrate: failed to remove drained slug dir", "dir", src, "err", err)
	}
	if err := os.Remove(projectsDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warn("migrate: failed to remove drained projects dir", "dir", projectsDir, "err", err)
	}
	log.Info("migrate: done", "data_dir", dataDir)
	_ = io.Discard // silence the stdlib import when verbose logging is trimmed
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
  migrate Flatten a legacy projects/<slug>/ layout into the v0.2 shape.
          Usage: tickets_please migrate <repo-path> [--dry-run]
  help    Show this message.

See SPEC.md and README.md (Wiring up MCP) for setup details.`)
}

const dataDirReadme = "# .tickets_please/ — the data directory\n\n" +
	"This directory is the on-disk store for everything tickets_please tracks.\n" +
	"It is intentionally human-readable and committed to git so the project's\n" +
	"ticket history travels with the repo.\n\n" +
	"Post-v0.2 layout (one project per data dir):\n\n" +
	"- `project.yaml`, `summary.md`, `summary.embedding.json` — the project record.\n" +
	"- `phases/<NNN-slug>/{phase.yaml,summary.md,...}` — optional phases.\n" +
	"- `tickets/<NNN-slug>/{ticket.yaml,body.md,comments/...}` — phase-less tickets.\n" +
	"- `agents/<session-uuid>.yaml` — one file per agent session (active or expired).\n" +
	"- `.staging/` — transient atomicity scratch dir; safe to delete when no server\n" +
	"  is running. Gitignored.\n\n" +
	"See `../SPEC.md` (Data layout) for the canonical schema. Repos still on the\n" +
	"v0.1 `projects/<slug>/` shape can be flattened with `tickets_please migrate`.\n"
