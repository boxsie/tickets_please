package svc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/google/uuid"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// freshServiceWithCfg lets individual tests tune cache + git behavior without
// each redeclaring the full config block.
func freshServiceWithCfg(t *testing.T, cfg config.Config) *Service {
	t.Helper()
	if cfg.DataDir == "" {
		cfg.DataDir = t.TempDir()
	}
	if cfg.LockTimeoutSeconds == 0 {
		cfg.LockTimeoutSeconds = 5
	}
	if cfg.AgentSessionTTLMinutes == 0 {
		cfg.AgentSessionTTLMinutes = 60
	}
	if cfg.AgentSessionMaxMinutes == 0 {
		cfg.AgentSessionMaxMinutes = 240
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// validSummary returns a 200+ char project summary.
func validSummary() string {
	return strings.Repeat("This project does X with constraints Y and Z. ", 6)
}

// authedCtx registers an agent and returns a context with the session id
// attached, plus the agent. Used by every mutating-method test.
func authedCtx(t *testing.T, s *Service) (context.Context, *domain.Agent) {
	t.Helper()
	ctx := context.Background()
	id, _, err := s.RegisterAgent(ctx, "test:agent-"+uuid.NewString()[:6], "Tester", nil, 0)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	a, err := s.GetAgent(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return WithSessionID(ctx, id), a
}

func TestCreateProject_Happy(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	p, err := s.CreateProject(ctx, "alpha", "Alpha", "An alpha project", validSummary())
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.Slug != "alpha" || p.Name != "Alpha" || p.ID == "" {
		t.Fatalf("unexpected project: %+v", p)
	}
	if p.CreatedBy == nil || p.CreatedBy.Name == "" {
		t.Fatalf("expected created_by attribution: %+v", p.CreatedBy)
	}

	// Files exist on disk.
	for _, f := range []string{"project.yaml", "summary.md"} {
		path := filepath.Join(s.Store.Root, "projects", "alpha", f)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
}

func TestCreateProject_RejectsShortSummary(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	_, err := s.CreateProject(ctx, "alpha", "Alpha", "", "too short")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestCreateProject_RejectsBadSlug(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	for _, slug := range []string{"", "a", "Alpha", "-leading", "trailing-", "has space", "exclaim!"} {
		_, err := s.CreateProject(ctx, slug, "n", "d", validSummary())
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("slug %q: expected ErrInvalidArgument, got %v", slug, err)
		}
	}
}

func TestCreateProject_DuplicateSlug(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "A", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateProject(ctx, "alpha", "B", "", validSummary())
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestCreateProject_RequiresSession(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	_, err := s.CreateProject(context.Background(), "alpha", "n", "", validSummary())
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestCreateProject_AutoCommitsInGitRepo(t *testing.T) {
	repoDir := t.TempDir()
	if _, err := git.PlainInit(repoDir, false); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		DataDir:    filepath.Join(repoDir, ".tickets_please"),
		AutoCommit: true,
	}
	s := freshServiceWithCfg(t, cfg)
	ctx, agent := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(commit.Message, "create project alpha") {
		t.Fatalf("commit message: %q", commit.Message)
	}
	if commit.Author.Name != agent.Name {
		t.Fatalf("commit author: %q (want %q)", commit.Author.Name, agent.Name)
	}
}

func TestGetProject_LazyLoadsThenHits(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	// Cache should be empty since CreateProject doesn't auto-load.
	if s.Cache.Len() != 0 {
		t.Fatalf("expected cache empty after CreateProject, got %d", s.Cache.Len())
	}

	p, err := s.GetProject(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug != "alpha" {
		t.Fatalf("got slug %q", p.Slug)
	}
	if s.Cache.Len() != 1 {
		t.Fatalf("expected 1 cached, got %d", s.Cache.Len())
	}

	// Get by id also resolves.
	p2, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p2.ID != p.ID {
		t.Fatal("id lookup mismatch")
	}
}

func TestListProjects_DoesNotLazyLoad(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, "bravo", "Bravo", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	beforeLen := s.Cache.Len()

	out, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(out))
	}
	if s.Cache.Len() != beforeLen {
		t.Fatalf("ListProjects mutated cache size: %d -> %d", beforeLen, s.Cache.Len())
	}
}

func TestUpdateProject_ShortSummaryRejected(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	bad := "too short"
	_, err := s.UpdateProject(ctx, "alpha", domain.UpdateProjectInput{Summary: &bad})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestUpdateProject_ChangesNameAndSummary(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	newName := "Alpha v2"
	newSummary := strings.Repeat("y ", 150)
	p, err := s.UpdateProject(ctx, "alpha", domain.UpdateProjectInput{Name: &newName, Summary: &newSummary})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != newName {
		t.Fatalf("name not updated: %q", p.Name)
	}
	if p.Summary != newSummary {
		t.Fatalf("summary not updated")
	}

	// Disk has the new summary.
	disk, err := os.ReadFile(filepath.Join(s.Store.Root, "projects", "alpha", "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(disk), newSummary) {
		t.Fatalf("disk summary did not pick up update: %q", string(disk[:50]))
	}
}

func TestDeleteProject_RefusesActiveTickets(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	// Drop a ticket directly on disk in the `todo` column. We don't have
	// a CreateTicket method yet (T05 lands later), so we hand-wire the
	// minimum the cache needs to count it as active.
	tdir := filepath.Join(s.Store.Root, "projects", "alpha", "tickets", "001-stub")
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatal(err)
	}
	tr := &store.TicketRecord{
		ID:        uuid.NewString(),
		ProjectID: "x",
		Number:    1,
		Title:     "Stub",
		Column:    domain.ColumnTodo,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.WriteYAMLAtomic(filepath.Join(tdir, "ticket.yaml"), tr); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tdir, "body.md"), []byte("stub\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := s.DeleteProject(ctx, "alpha")
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition, got %v", err)
	}
}

func TestDeleteProject_HappyPath_RemovesViaStageOp(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(s.Store.Root, "projects", "alpha")
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteProject(ctx, "alpha"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("project dir still present: %v", err)
	}

	// Cache no longer holds it.
	if s.Cache.Len() != 0 {
		t.Fatalf("expected cache empty after delete, got %d", s.Cache.Len())
	}

	// Staging dir is empty (the StageOp's removal cleaned up after itself).
	stagingEntries, err := os.ReadDir(filepath.Join(s.Store.Root, ".staging"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stagingEntries) != 0 {
		t.Fatalf("expected empty .staging after DeleteProject, got %v", stagingEntries)
	}
}

func TestLoadProject_ReturnsPopulatedResult(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{ProjectIdleMinutes: 15})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	res, err := s.LoadProject(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if res.Project == nil || res.Project.Slug != "alpha" {
		t.Fatalf("unexpected project: %+v", res.Project)
	}
	if res.Handle == "" {
		t.Fatal("expected non-empty handle")
	}
	if res.ExpiresAt.Before(time.Now().Add(10 * time.Minute)) {
		t.Fatalf("ExpiresAt too soon: %v", res.ExpiresAt)
	}
	if res.TicketCount != 0 || res.ActiveTicketCount != 0 {
		t.Fatalf("expected zero counts, got %+v", res)
	}
}

func TestService_Close_StopsEvictor(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	// Already covered by t.Cleanup; just verify it's safe to call twice.
	s.Close()
	s.Close()
}
