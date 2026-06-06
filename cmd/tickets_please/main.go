// Single binary for tickets_please. Subcommands:
//
//	mcp     (default) — stdio MCP server. Wraps svc.Service as 31 MCP tools.
//	serve            — long-running HTTP MCP server (StreamableHTTP transport).
//	check            — integrity check + exit. Stub until T02.
//	init             — create the .tickets_please/ data dir scaffold.
//	migrate <repo>   — flatten a v0.1 layout (.tickets_please/projects/<slug>/*)
//	                  to the v0.2 single-project shape (.tickets_please/*).
//	grant-owner      — grant a user a role on a project directly (recovery).
//	list-users       — list the central user registry.
//	list-memberships — list memberships on a project.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"tickets_please/internal/config"
	"tickets_please/internal/eventbus"
	tplog "tickets_please/internal/log"
	"tickets_please/internal/mcptools"
	"tickets_please/internal/svc"
	"tickets_please/internal/web"
)

// version is the MCP server version reported to clients. Bump when meaningful
// behaviour changes; semver-ish.
const version = "0.3.0"

// totalTools is the canonical tool count exposed by the MCP server. Mirrored
// in the SPEC.md "MCP server" section table; if you change this, update both.
const totalTools = 35

func main() {
	sub := "mcp"
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}

	// Tee logs into an in-process ring buffer in addition to stderr so the
	// /logs page (only mounted under `serve`) can render recent records.
	// The ring is process-global; passing it through deps keeps the wiring
	// explicit instead of leaning on a package-level singleton.
	logRing := tplog.NewRing(tplog.DefaultCapacity)
	logger := slog.New(tplog.NewMultiHandler(
		slog.NewJSONHandler(os.Stderr, nil),
		tplog.NewRingHandler(logRing, nil),
	))
	// Make slog.Default() route through the same multi-handler so any code
	// that didn't get the explicit logger threaded through (or uses the
	// package-default for convenience) still lands in the ring buffer.
	slog.SetDefault(logger)

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
	case "serve":
		if err := runServe(os.Args[2:], cfg, logger, logRing); err != nil {
			logger.Error("serve failed", "err", err)
			os.Exit(1)
		}
	case "check":
		logger.Info("integrity check not implemented yet, see T02")
	case "init":
		if err := runInit(logger, cfg); err != nil {
			logger.Error("init failed", "err", err)
			os.Exit(1)
		}
	case "grant-owner":
		if err := runGrantOwner(os.Args[2:], cfg, logger); err != nil {
			logger.Error("grant-owner failed", "err", err)
			os.Exit(1)
		}
	case "list-users":
		if err := runListUsers(os.Args[2:], cfg, logger); err != nil {
			logger.Error("list-users failed", "err", err)
			os.Exit(1)
		}
	case "list-memberships":
		if err := runListMemberships(os.Args[2:], cfg, logger); err != nil {
			logger.Error("list-memberships failed", "err", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q (use one of: mcp, serve, check, init, migrate, grant-owner, list-users, list-memberships)\n", sub)
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
	agentID, expiresAt, err := s.RegisterAgent(ctx, sess.AgentKey, sess.AgentName, sess.Metadata, 0, "")
	if err != nil {
		return fmt.Errorf("register mcp agent: %w", err)
	}
	sess.AgentID = agentID
	sess.ExpiresAt = expiresAt

	// MCP_PROJECT_SLUG lets stdio clients pre-bind a default project without
	// calling register_agent. Tools that take project_id_or_slug then fall
	// back to this slug when the param is omitted.
	if slug := strings.TrimSpace(os.Getenv("MCP_PROJECT_SLUG")); slug != "" {
		sess.ProjectSlug = slug
	}

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

// runServe boots the same Service + MCPServer wiring as runMCP but exposes it
// over HTTP via mcp-go's StreamableHTTPServer. Per-connection sessions are
// handled natively by the library (the MCP server's per-call context already
// carries the client session — register_agent attaches the agent identity).
//
// HTTP layout:
//
//	/mcp      → mcp-go StreamableHTTP handler
//	/healthz  → 200 "ok" plaintext liveness probe
//
// Localhost-only by default, no TLS, no auth — out of scope for v1.
func runServe(args []string, cfg config.Config, log *slog.Logger, logRing *tplog.Ring) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8765", "HTTP listen address")
	dataRoot := fs.String("data-root", "", "override central data root (default: cfg.DataRoot)")
	remoteProjectRoot := fs.String("remote-project-root", "", "override root under which create_project may materialise missing project paths (default: cfg.RemoteProjectRoot)")
	dev := fs.Bool("dev", false, "developer mode: reparse web templates and static files from disk on every request")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse serve flags: %w", err)
	}
	if *dataRoot != "" {
		cfg.DataRoot = *dataRoot
	}
	if *remoteProjectRoot != "" {
		cfg.RemoteProjectRoot = *remoteProjectRoot
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	s, err := svc.New(cfg)
	if err != nil {
		return fmt.Errorf("build service: %w", err)
	}
	defer s.Close()

	registry := mcptools.NewRegistry(cfg)

	mcpServer := mcpserver.NewMCPServer(
		"tickets_please",
		version,
		mcpserver.WithInstructions(mcptools.ServerInstructions),
	)
	tools := mcptools.NewTools(s, registry, log)
	tools.Remote = true
	tools.RegisterAll(mcpServer)

	httpMCP := mcpserver.NewStreamableHTTPServer(mcpServer)

	mux := http.NewServeMux()
	mux.Handle("/mcp", httpMCP)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	// One shared realtime bus: svc publishes mutations into it, /sse fans them
	// out as Datastar patches.
	bus := eventbus.NewBus()
	s.SetPublisher(bus)
	web.Mount(mux, web.Deps{Service: s, Logger: log, Cfg: cfg, Dev: *dev, Logs: logRing, Bus: bus})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Info("http mcp server starting",
		"addr", *addr,
		"data_root", cfg.DataRoot,
		"remote_project_root", cfg.RemoteProjectRoot,
		"tools", totalTools,
		"web_ui", true,
		"dev", *dev,
	)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}
		<-errCh
		return nil
	case err := <-errCh:
		return fmt.Errorf("http listen: %w", err)
	}
}

// runInit creates the per-repo data-dir scaffold under cfg.DataDir and the
// central agent registry under cfg.DataRoot. Post-T003:
//
//   - cfg.DataDir (.tickets_please/): only `.staging/` is created here.
//     Project content (project.yaml, phases/, tickets/) lands at the root
//     once a project is created.
//   - cfg.DataRoot (~/.tickets_please/): `agents/` + `.staging/` are created
//     here for the central agent registry.
func runInit(logger *slog.Logger, cfg config.Config) error {
	dataDir := cfg.DataDir
	if dataDir == "" {
		return errors.New("data_dir is empty")
	}

	// Per-repo scaffold: only .staging/ (agents/ moved to DataRoot).
	stagingDir := filepath.Join(dataDir, ".staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", stagingDir, err)
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

	gitignore := filepath.Join(dataDir, ".gitignore")
	if _, err := os.Stat(gitignore); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(gitignore, []byte(dataDirGitignore), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", gitignore, err)
		}
		logger.Info("wrote data dir gitignore", "path", gitignore)
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", gitignore, err)
	}

	logger.Info("data dir ready", "data_dir", dataDir)

	// Central agent registry scaffold under DataRoot.
	dataRoot := cfg.DataRoot
	if dataRoot == "" {
		dataRoot = cfg.DataDir + "-central"
		logger.Warn("data_root is empty; using fallback", "data_root", dataRoot)
	}
	for _, sd := range []string{"agents", ".staging"} {
		full := filepath.Join(dataRoot, sd)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", full, err)
		}
	}
	logger.Info("central agent registry ready", "data_root", dataRoot)
	return nil
}

// runMigrate flattens a v0.1 layout where project content lived under
// `.tickets_please/projects/<slug>/*` into the v0.2 shape, and (T003) also
// moves any per-repo agent yamls from `.tickets_please/agents/*.yaml` into the
// central data root at `<dataRoot>/agents/`. The migrate is idempotent:
// running it on an already-migrated repo is a no-op.
//
// One repo holds at most one project (post-flatten), so a multi-project legacy
// layout aborts with an error pointing the operator at the conflict.
//
// Flags:
//
//	--dry-run       Print what would happen without touching the filesystem.
//	--data-root     Central data root for agents (default: ~/.tickets_please).
func runMigrate(args []string, log *slog.Logger) error {
	dryRun := false
	var repoPath string
	// dataRoot defaults to empty; we resolve it below (after parsing flags) so
	// we can fall back to the config default.
	dataRoot := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--data-root":
			if i+1 >= len(args) {
				return errors.New("--data-root requires a value")
			}
			i++
			dataRoot = args[i]
		case len(a) > len("--data-root=") && a[:len("--data-root=")] == "--data-root=":
			dataRoot = a[len("--data-root="):]
		case a == "-h" || a == "--help":
			fmt.Println("usage: tickets_please migrate <repo-path> [--dry-run] [--data-root <path>]")
			return nil
		default:
			if repoPath != "" {
				return fmt.Errorf("unexpected extra argument %q (only one repo path allowed)", a)
			}
			repoPath = a
		}
	}
	if repoPath == "" {
		return errors.New("repo path required: tickets_please migrate <repo-path> [--dry-run] [--data-root <path>]")
	}

	// Resolve data root: flag > env/config default.
	if dataRoot == "" {
		// Try to get it from a loaded config; if that fails, derive from home.
		if cfg, err := config.Load(); err == nil && cfg.DataRoot != "" {
			dataRoot = cfg.DataRoot
		} else {
			if home, err := os.UserHomeDir(); err == nil {
				dataRoot = filepath.Join(home, ".tickets_please")
			} else {
				dataRoot = "./.tickets_please-central"
				log.Warn("migrate: cannot determine home dir; using fallback data-root", "data_root", dataRoot)
			}
		}
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

	// --- Phase 1: flatten projects/<slug>/* → dataDir root ---

	projectsDir := filepath.Join(dataDir, "projects")
	info, err := os.Stat(projectsDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		log.Info("migrate: already flat (no projects/ subdir)", "data_dir", dataDir)
	case err != nil:
		return fmt.Errorf("stat %s: %w", projectsDir, err)
	case !info.IsDir():
		return fmt.Errorf("%s exists but is not a directory", projectsDir)
	default:
		// projects/ exists — check for slug subdirs.
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
		case 1:
			slug := slugDirs[0]
			src := filepath.Join(projectsDir, slug)
			srcEntries, err := os.ReadDir(src)
			if err != nil {
				return fmt.Errorf("read %s: %w", src, err)
			}

			// `.lock` is a runtime artefact; drop it rather than hoist.
			skip := map[string]bool{".lock": true}

			// Collision check.
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
				"slug", slug, "from", src, "to", dataDir,
				"entries", len(srcEntries), "dry_run", dryRun,
			)
			if dryRun {
				for _, e := range srcEntries {
					if skip[e.Name()] {
						log.Info("migrate: would skip (runtime file)", "name", e.Name())
						continue
					}
					log.Info("migrate: would move", "name", e.Name())
				}
			} else {
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
			}
		default:
			return fmt.Errorf(
				"migrate: %s has %d project subdirs (%v); the new layout is one project per repo — split or pick one before migrating",
				projectsDir, len(slugDirs), slugDirs,
			)
		}
	}

	// --- Phase 2: move per-repo agents/*.yaml → <dataRoot>/agents/ ---

	repoAgentsDir := filepath.Join(dataDir, "agents")
	centralAgentsDir := filepath.Join(dataRoot, "agents")

	agentEntries, err := os.ReadDir(repoAgentsDir)
	if errors.Is(err, os.ErrNotExist) {
		log.Info("migrate: no per-repo agents dir; nothing to move", "path", repoAgentsDir)
	} else if err != nil {
		return fmt.Errorf("read %s: %w", repoAgentsDir, err)
	} else {
		if !dryRun {
			if err := os.MkdirAll(centralAgentsDir, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", centralAgentsDir, err)
			}
		}
		moved := 0
		skipped := 0
		for _, e := range agentEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			src := filepath.Join(repoAgentsDir, e.Name())
			dst := filepath.Join(centralAgentsDir, e.Name())
			if dryRun {
				if _, statErr := os.Stat(dst); statErr == nil {
					log.Info("migrate: would skip agent (already in central path)", "file", e.Name())
				} else {
					log.Info("migrate: would move agent to central path", "file", e.Name(), "dst", dst)
				}
				continue
			}
			if _, statErr := os.Stat(dst); statErr == nil {
				log.Info("migrate: skip agent (already in central path)", "file", e.Name())
				skipped++
				continue
			}
			if err := os.Rename(src, dst); err != nil {
				return fmt.Errorf("migrate agent %s: %w", e.Name(), err)
			}
			moved++
		}
		if !dryRun {
			log.Info("migrate: agents moved to central path",
				"moved", moved, "skipped", skipped, "data_root", dataRoot)
			// Clean up per-repo agents dir (and .gitkeep if present).
			_ = os.Remove(filepath.Join(repoAgentsDir, ".gitkeep"))
			if err := os.Remove(repoAgentsDir); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Warn("migrate: failed to remove per-repo agents dir", "dir", repoAgentsDir, "err", err)
			}
		}
	}

	log.Info("migrate: done", "data_dir", dataDir, "data_root", dataRoot)
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
  serve   Run a long-running HTTP MCP server (StreamableHTTP transport).
          Usage: tickets_please serve [--addr :8765] [--data-root <path>]
          Mounts the MCP transport at /mcp and a /healthz liveness probe.
  check   Run an integrity walk over the data dir and exit.
  init    Create the .tickets_please/ data dir scaffold.
  migrate Flatten a legacy projects/<slug>/ layout into the v0.2 shape,
          and move per-repo agents to the central data root (~/.tickets_please/).
          Usage: tickets_please migrate <repo-path> [--dry-run] [--data-root <path>]
  grant-owner       Grant a user a role on a project directly (lock-out recovery).
          Usage: tickets_please grant-owner --user-id <id> --project <id|slug> [--role owner]
  list-users        List the central user registry (id, name, email, providers).
  list-memberships  List every membership on a project.
          Usage: tickets_please list-memberships --project <id|slug>
  help    Show this message.

See SPEC.md and README.md (Wiring up MCP) for setup details.`)
}

const dataDirReadme = "# .tickets_please/ — the per-repo data directory\n\n" +
	"This directory is the on-disk store for everything tickets_please tracks\n" +
	"about this repo's project. It is intentionally human-readable and committed\n" +
	"to git so the project's ticket history travels with the repo.\n\n" +
	"Post-v0.3 layout (one project per data dir):\n\n" +
	"- `project.yaml`, `summary.md`, `summary.embedding.json` — the project record.\n" +
	"- `phases/<NNN-slug>/{phase.yaml,summary.md,...}` — optional phases.\n" +
	"- `tickets/<NNN-slug>/{ticket.yaml,body.md,comments/...}` — phase-less tickets.\n" +
	"- `.staging/` — transient atomicity scratch dir; safe to delete when no server\n" +
	"  is running. Gitignored.\n\n" +
	"Agent sessions (active or expired) are stored centrally at\n" +
	"`~/.tickets_please/agents/<session-uuid>.yaml` (configurable via `data_root`)\n" +
	"so a single server instance can serve multiple repos without each one holding\n" +
	"a copy of the agent registry.\n\n" +
	"## Cold-starting a fresh repo\n\n" +
	"If this dir has only `.staging/` and this README, there's no project yet.\n" +
	"From any MCP client (HTTP or stdio) just call:\n\n" +
	"1. `create_project` with `slug`, `name`, and a substantive `summary`\n" +
	"   (≥200 chars — load-bearing context for future work). On a remote\n" +
	"   (HTTP) server, that's all you need: the server stores the project at\n" +
	"   `<remote_project_root>/<slug>` automatically. Stdio clients also pass\n" +
	"   `project_path`, the absolute path of this repo, so `project.yaml`\n" +
	"   lands here. This is the only mutation that doesn't require a\n" +
	"   registered session — the bootstrap escape valve. `created_by` is\n" +
	"   left empty for the bootstrap call.\n" +
	"2. `register_agent` to bind your session. Remote clients pass\n" +
	"   `project_slug`; stdio clients pass `project_path`. Every subsequent\n" +
	"   mutation gets attributed.\n\n" +
	"See `../SPEC.md` (Data layout) for the canonical schema. Repos still on the\n" +
	"v0.1 `projects/<slug>/` shape can be flattened with `tickets_please migrate`.\n"

const dataDirGitignore = "*.embedding.json\n.staging/\n"
