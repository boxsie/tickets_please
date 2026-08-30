package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

func TestCommitHookRunZeroOneManyInProgressTickets(t *testing.T) {
	tests := []struct {
		name       string
		tickets    int
		wantSuffix string
		wantTip    string
	}{
		{name: "zero", tickets: 0, wantSuffix: "feature commit\n", wantTip: "found 0 in-progress tickets"},
		{name: "one", tickets: 1, wantSuffix: "feature commit\n\ntest-project/001\n"},
		{name: "many", tickets: 2, wantSuffix: "feature commit\n", wantTip: "found 2 in-progress tickets"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cfg, agentID := seedCommitHookRepo(t)
			for number := 1; number <= tt.tickets; number++ {
				seedInProgressTicket(t, repo, number, agentID)
			}

			messagePath := filepath.Join(repo, "COMMIT_EDITMSG")
			if err := os.WriteFile(messagePath, []byte("feature commit\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Chdir(repo)
			var stderr bytes.Buffer
			if err := appendCurrentTicketRef(cfg, messagePath, &stderr); err != nil {
				t.Fatalf("appendCurrentTicketRef: %v", err)
			}

			got, err := os.ReadFile(messagePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.wantSuffix {
				t.Fatalf("commit message = %q, want %q", got, tt.wantSuffix)
			}
			if tt.wantTip != "" && !strings.Contains(stderr.String(), tt.wantTip) {
				t.Fatalf("stderr = %q, want tip containing %q", stderr.String(), tt.wantTip)
			}
		})
	}
}

func TestCommitHookRunIsIdempotent(t *testing.T) {
	repo, cfg, agentID := seedCommitHookRepo(t)
	seedInProgressTicket(t, repo, 7, agentID)
	messagePath := filepath.Join(repo, "COMMIT_EDITMSG")
	if err := os.WriteFile(messagePath, []byte("feature commit"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	for range 2 {
		if err := appendCurrentTicketRef(cfg, messagePath, &bytes.Buffer{}); err != nil {
			t.Fatalf("appendCurrentTicketRef: %v", err)
		}
	}
	got, err := os.ReadFile(messagePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "feature commit\n\ntest-project/007\n" {
		t.Fatalf("idempotent commit message = %q", got)
	}
}

func TestCommitHookUsesLatestMoveToInProgressAsOwnership(t *testing.T) {
	repo, cfg, agentID := seedCommitHookRepo(t)
	seedInProgressTicket(t, repo, 1, "different-agent")
	ticketDir := filepath.Join(repo, ".tickets_please", "tickets", "001-ticket")
	to := domain.ColumnInProgress
	comment := &store.CommentRecord{
		ID:            "latest-move",
		TicketID:      "ticket-1",
		Kind:          domain.CommentKindSystemMove,
		AuthorAgentID: &agentID,
		ToColumn:      &to,
		CreatedAt:     time.Now(),
	}
	if err := store.WriteMarkdown(filepath.Join(ticketDir, "comments", "9999-latest-system_move.md"), comment, "picked up"); err != nil {
		t.Fatal(err)
	}

	messagePath := filepath.Join(repo, "COMMIT_EDITMSG")
	if err := os.WriteFile(messagePath, []byte("feature commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if err := appendCurrentTicketRef(cfg, messagePath, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(messagePath)
	if !strings.Contains(string(got), "test-project/001") {
		t.Fatalf("latest pickup agent did not own ticket: %q", got)
	}
}

func TestCommitHookInstallLifecycle(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := installCommitHook(repo, false, &out); err != nil {
		t.Fatalf("installCommitHook: %v", err)
	}
	hookPath := filepath.Join(repo, ".git", "hooks", commitHookName)
	body, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), commitHookMarker) || !strings.Contains(string(body), `tickets_please commit-hook run "$@"`) {
		t.Fatalf("installed hook body = %q", body)
	}
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed hook is not executable: %s", info.Mode())
	}

	out.Reset()
	if err := commitHookStatus(repo, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "installed at") {
		t.Fatalf("status output = %q", out.String())
	}

	out.Reset()
	if err := uninstallCommitHook(repo, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hookPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed hook remains after uninstall: %v", err)
	}
}

func TestCommitHookDoesNotClobberExistingHook(t *testing.T) {
	repo := t.TempDir()
	hooksDir := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, commitHookName)
	original := []byte("#!/bin/sh\necho existing\n")
	if err := os.WriteFile(hookPath, original, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := installCommitHook(repo, false, &bytes.Buffer{}); err == nil {
		t.Fatal("install should refuse an existing hook without --force-append")
	}
	got, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("existing hook was changed: %q", got)
	}

	if err := installCommitHook(repo, true, &bytes.Buffer{}); err != nil {
		t.Fatalf("force append install: %v", err)
	}
	backupPath := hookPath + ".tickets_please.previous"
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backup, original) {
		t.Fatalf("preserved hook = %q, want %q", backup, original)
	}
	if err := uninstallCommitHook(repo, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("restored hook = %q, want %q", restored, original)
	}
}

func seedCommitHookRepo(t *testing.T) (string, config.Config, string) {
	t.Helper()
	repo := t.TempDir()
	dataDir := filepath.Join(repo, ".tickets_please")
	dataRoot := filepath.Join(repo, "central")
	for _, dir := range []string{filepath.Join(dataDir, "tickets"), filepath.Join(dataDir, "phases"), filepath.Join(repo, ".git", "hooks")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	project := &store.ProjectRecord{ID: "project-id", Slug: "test-project", Name: "Test Project", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.WriteYAMLAtomic(filepath.Join(dataDir, "project.yaml"), project); err != nil {
		t.Fatal(err)
	}

	agentStore, err := store.NewAgentStore(dataRoot, 1)
	if err != nil {
		t.Fatal(err)
	}
	agentID := "active-session"
	agent := &store.AgentRecord{
		ID:         agentID,
		Key:        "codex:stable",
		Name:       "Clio",
		Metadata:   map[string]string{"project_path": repo},
		CreatedAt:  time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
		LastSeenAt: time.Now(),
	}
	if err := agentStore.RegisterAgent(t.Context(), agent); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{DataDir: dataDir, DataRoot: dataRoot, LockTimeoutSeconds: 1, AutoCommit: false}
	return repo, cfg, agentID
}

func seedInProgressTicket(t *testing.T, repo string, number int, ownerID string) {
	t.Helper()
	ticketID := fmt.Sprintf("ticket-%d", number)
	numberText := fmt.Sprintf("%03d", number)
	ticketDir := filepath.Join(repo, ".tickets_please", "tickets", numberText+"-ticket")
	if err := os.MkdirAll(filepath.Join(ticketDir, "comments"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &store.TicketRecord{
		ID:               ticketID,
		ProjectID:        "project-id",
		Number:           number,
		Title:            "Ticket",
		Column:           domain.ColumnInProgress,
		CreatedByAgentID: &ownerID,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := store.WriteYAMLAtomic(filepath.Join(ticketDir, "ticket.yaml"), rec); err != nil {
		t.Fatal(err)
	}
	to := domain.ColumnInProgress
	comment := &store.CommentRecord{
		ID:            "move-" + numberText,
		TicketID:      ticketID,
		Kind:          domain.CommentKindSystemMove,
		AuthorAgentID: &ownerID,
		ToColumn:      &to,
		CreatedAt:     time.Now(),
	}
	if err := store.WriteMarkdown(filepath.Join(ticketDir, "comments", "0001-move-system_move.md"), comment, "picked up"); err != nil {
		t.Fatal(err)
	}
}
