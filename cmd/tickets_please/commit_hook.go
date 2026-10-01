package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

const (
	commitHookMarker = "# tickets_please managed prepare-commit-msg hook v1"
	commitHookName   = "prepare-commit-msg"
)

func runCommitHook(args []string, cfg config.Config, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: tickets_please commit-hook <install|run|status|uninstall>")
	}

	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("commit-hook install", flag.ContinueOnError)
		fs.SetOutput(stderr)
		repo := fs.String("repo", ".", "repository working tree")
		forceAppend := fs.Bool("force-append", false, "preserve and wrap an existing hook")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("usage: tickets_please commit-hook install [--repo PATH] [--force-append]")
		}
		return installCommitHook(*repo, *forceAppend, stdout)

	case "run":
		if len(args) < 2 {
			return errors.New("usage: tickets_please commit-hook run <commit-msg-file> [source] [sha]")
		}
		return appendCurrentTicketRef(cfg, args[1], stderr)

	case "status", "uninstall":
		fs := flag.NewFlagSet("commit-hook "+args[0], flag.ContinueOnError)
		fs.SetOutput(stderr)
		repo := fs.String("repo", ".", "repository working tree")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("usage: tickets_please commit-hook %s [--repo PATH]", args[0])
		}
		if args[0] == "status" {
			return commitHookStatus(*repo, stdout)
		}
		return uninstallCommitHook(*repo, stdout)

	default:
		return fmt.Errorf("unknown commit-hook action %q (use one of: install, run, status, uninstall)", args[0])
	}
}

func installCommitHook(repo string, forceAppend bool, stdout io.Writer) error {
	hookPath, err := prepareCommitHookPath(repo)
	if err != nil {
		return err
	}
	backupPath := hookPath + ".tickets_please.previous"

	existing, err := os.ReadFile(hookPath)
	switch {
	case err == nil && strings.Contains(string(existing), commitHookMarker):
		fmt.Fprintf(stdout, "tickets_please commit hook already installed at %s\n", hookPath)
		return nil
	case err == nil && !forceAppend:
		return fmt.Errorf("prepare-commit-msg hook already exists at %s; left untouched (use --force-append to preserve and wrap it)", hookPath)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("read existing prepare-commit-msg hook: %w", err)
	}

	wrapped := err == nil
	if wrapped {
		if _, statErr := os.Stat(backupPath); statErr == nil {
			return fmt.Errorf("refusing to overwrite existing hook backup at %s", backupPath)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat hook backup: %w", statErr)
		}
		if err := os.Rename(hookPath, backupPath); err != nil {
			return fmt.Errorf("preserve existing prepare-commit-msg hook: %w", err)
		}
	}

	if err := writeExecutableAtomic(hookPath, []byte(managedCommitHookScript)); err != nil {
		if wrapped {
			_ = os.Rename(backupPath, hookPath)
		}
		return err
	}

	if wrapped {
		fmt.Fprintf(stdout, "installed tickets_please commit hook at %s (existing hook preserved and wrapped)\n", hookPath)
	} else {
		fmt.Fprintf(stdout, "installed tickets_please commit hook at %s\n", hookPath)
	}
	return nil
}

func commitHookStatus(repo string, stdout io.Writer) error {
	hookPath, err := prepareCommitHookPath(repo)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(hookPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stdout, "not installed (%s)\n", hookPath)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read prepare-commit-msg hook: %w", err)
	}
	if !strings.Contains(string(body), commitHookMarker) {
		fmt.Fprintf(stdout, "not installed; another prepare-commit-msg hook exists at %s\n", hookPath)
		return nil
	}
	if _, err := os.Stat(hookPath + ".tickets_please.previous"); err == nil {
		fmt.Fprintf(stdout, "installed (wrapping previous hook) at %s\n", hookPath)
	} else {
		fmt.Fprintf(stdout, "installed at %s\n", hookPath)
	}
	return nil
}

func uninstallCommitHook(repo string, stdout io.Writer) error {
	hookPath, err := prepareCommitHookPath(repo)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(hookPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stdout, "tickets_please commit hook is not installed at %s\n", hookPath)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read prepare-commit-msg hook: %w", err)
	}
	if !strings.Contains(string(body), commitHookMarker) {
		fmt.Fprintf(stdout, "tickets_please commit hook is not installed; left existing hook untouched at %s\n", hookPath)
		return nil
	}

	backupPath := hookPath + ".tickets_please.previous"
	if _, err := os.Stat(backupPath); err == nil {
		if err := os.Remove(hookPath); err != nil {
			return fmt.Errorf("remove tickets_please hook wrapper: %w", err)
		}
		if err := os.Rename(backupPath, hookPath); err != nil {
			return fmt.Errorf("restore previous prepare-commit-msg hook: %w", err)
		}
		fmt.Fprintf(stdout, "uninstalled tickets_please commit hook and restored the previous hook at %s\n", hookPath)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat previous prepare-commit-msg hook: %w", err)
	}

	if err := os.Remove(hookPath); err != nil {
		return fmt.Errorf("remove tickets_please commit hook: %w", err)
	}
	fmt.Fprintf(stdout, "uninstalled tickets_please commit hook from %s\n", hookPath)
	return nil
}

const managedCommitHookScript = `#!/bin/sh
` + commitHookMarker + `
previous="$0.tickets_please.previous"
if [ -x "$previous" ]; then
  "$previous" "$@" || exit $?
fi
exec tickets_please commit-hook run "$@"
`

func prepareCommitHookPath(repo string) (string, error) {
	repoRoot, err := canonicalPath(repo)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	gitPath := filepath.Join(repoRoot, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", fmt.Errorf("locate .git under %s: %w", repoRoot, err)
	}

	gitDir := gitPath
	if !info.IsDir() {
		body, err := os.ReadFile(gitPath)
		if err != nil {
			return "", fmt.Errorf("read gitdir file: %w", err)
		}
		line := strings.TrimSpace(string(body))
		if !strings.HasPrefix(line, "gitdir:") {
			return "", fmt.Errorf("unsupported .git file at %s", gitPath)
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(repoRoot, gitDir)
		}
	}

	hooksDir := filepath.Join(filepath.Clean(gitDir), "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return "", fmt.Errorf("create git hooks directory: %w", err)
	}
	return filepath.Join(hooksDir, commitHookName), nil
}

func writeExecutableAtomic(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tickets-please-hook-*")
	if err != nil {
		return fmt.Errorf("create temporary hook: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary hook: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("make temporary hook executable: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary hook: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install prepare-commit-msg hook: %w", err)
	}
	return nil
}

func appendCurrentTicketRef(cfg config.Config, messagePath string, stderr io.Writer) error {
	message, err := os.ReadFile(messagePath)
	if err != nil {
		return fmt.Errorf("read commit message: %w", err)
	}
	repoRoot, err := canonicalPath(".")
	if err != nil {
		return fmt.Errorf("resolve repository path: %w", err)
	}

	dataDir := filepath.Join(repoRoot, ".tickets_please")
	project := &store.ProjectRecord{}
	if err := store.ReadYAML(filepath.Join(dataDir, "project.yaml"), project); err != nil {
		fmt.Fprintf(stderr, "tickets_please: no local project found; commit message left unchanged (%v)\n", err)
		return nil
	}
	if containsTicketRef(string(message), project.Slug) {
		return nil
	}

	// The commit path must be read-only: constructing the lightweight store
	// handles directly avoids creating directories or probing git/embedding state.
	st := &store.Store{Root: dataDir}
	agentStore := &store.AgentStore{Root: cfg.DataRoot, LockTimeoutSeconds: cfg.LockTimeoutSeconds}

	agentKey := strings.TrimSpace(cfg.MCPAgentKey)
	if agentKey == "" {
		agentKey, err = activeAgentKeyForRepo(agentStore, repoRoot, time.Now())
		if err != nil {
			fmt.Fprintf(stderr, "tickets_please: %v; commit message left unchanged\n", err)
			return nil
		}
	}
	agentIDs, err := agentIDsForKey(agentStore, agentKey)
	if err != nil {
		return err
	}

	refs, err := inProgressRefsForAgent(st, project.Slug, agentIDs)
	if err != nil {
		return err
	}
	if len(refs) != 1 {
		fmt.Fprintf(stderr, "tickets_please: found %d in-progress tickets for this agent; commit message left unchanged\n", len(refs))
		return nil
	}
	return appendFooter(messagePath, message, refs[0])
}

func activeAgentKeyForRepo(agentStore *store.AgentStore, repoRoot string, now time.Time) (string, error) {
	keys := make(map[string]struct{})
	err := agentStore.WalkAgents(func(rec *store.AgentRecord) error {
		if !rec.ExpiresAt.After(now) || strings.TrimSpace(rec.Key) == "" {
			return nil
		}
		bound := strings.TrimSpace(rec.Metadata["project_path"])
		if bound == "" {
			return nil
		}
		canonical, err := canonicalPath(bound)
		if err == nil && canonical == repoRoot {
			keys[rec.Key] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("read active agent sessions: %w", err)
	}
	if len(keys) != 1 {
		return "", fmt.Errorf("found %d active agent keys bound to this repository", len(keys))
	}
	for key := range keys {
		return key, nil
	}
	panic("unreachable")
}

func agentIDsForKey(agentStore *store.AgentStore, key string) (map[string]struct{}, error) {
	ids := make(map[string]struct{})
	// Archived sessions too: an earlier session under the same key can still
	// own an in-progress ticket.
	if err := agentStore.WalkAllAgents(func(rec *store.AgentRecord) error {
		if rec.Key == key {
			ids[rec.ID] = struct{}{}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("resolve agent sessions for key %q: %w", key, err)
	}
	return ids, nil
}

func inProgressRefsForAgent(st *store.Store, projectSlug string, agentIDs map[string]struct{}) ([]string, error) {
	refs := make([]string, 0)
	err := st.WalkTickets(projectSlug, func(ticketDir, _ string, rec *store.TicketRecord) error {
		if rec.Column != domain.ColumnInProgress {
			return nil
		}

		ownerID := ""
		if err := st.WalkComments(ticketDir, func(comment *store.CommentRecord, _ string) error {
			if comment.Kind == domain.CommentKindSystemMove && comment.ToColumn != nil && *comment.ToColumn == domain.ColumnInProgress && comment.AuthorAgentID != nil {
				ownerID = *comment.AuthorAgentID
			}
			return nil
		}); err != nil {
			return err
		}
		if ownerID == "" && rec.CreatedByAgentID != nil {
			ownerID = *rec.CreatedByAgentID
		}
		if _, ok := agentIDs[ownerID]; ok {
			refs = append(refs, fmt.Sprintf("%s/%03d", projectSlug, rec.Number))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list in-progress tickets: %w", err)
	}
	sort.Strings(refs)
	return refs, nil
}

func containsTicketRef(message, projectSlug string) bool {
	re := regexp.MustCompile(`(?m)(^|[[:space:]])` + regexp.QuoteMeta(projectSlug) + `/[0-9]+([[:space:]]|$)`)
	return re.MatchString(message)
}

func appendFooter(path string, original []byte, ref string) error {
	prefix := "\n\n"
	if len(original) == 0 {
		prefix = ""
	} else if strings.HasSuffix(string(original), "\n\n") {
		prefix = ""
	} else if strings.HasSuffix(string(original), "\n") {
		prefix = "\n"
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open commit message for append: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintf(f, "%s%s\n", prefix, ref); err != nil {
		return fmt.Errorf("append ticket reference: %w", err)
	}
	return nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}
