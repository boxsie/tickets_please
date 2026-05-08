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
// each redeclaring the full config block. Tests use the deterministic fake
// embedding provider so the worker can run end-to-end without an Ollama
// sidecar process.
func freshServiceWithCfg(t *testing.T, cfg config.Config) *Service {
	t.Helper()
	if cfg.DataDir == "" {
		// Lay the data dir out the way production stdio does: <repo>/.tickets_please.
		// CreateProject's post-create RegisterProjectMount only fires when the
		// data dir's basename is `.tickets_please`, and per-mount workers
		// require a mount — without this every legacy test would be writing
		// into nothing.
		cfg.DataDir = filepath.Join(t.TempDir(), ".tickets_please")
		if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
			t.Fatalf("mkdir DataDir: %v", err)
		}
	}
	// Always use an isolated DataRoot per test so agent yamls never land in the
	// user's real ~/.tickets_please.
	if cfg.DataRoot == "" {
		cfg.DataRoot = t.TempDir()
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
	s, err := NewWithEmbed(cfg, newFakeEmbed())
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
		path := filepath.Join(s.Store.Root, f)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
}

// CreateProject stamps the embed provider/model from cfg into the on-disk
// project.yaml so each project carries its own embedding declaration.
func TestCreateProject_StampsEmbedFromCfg(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{
		EmbedProvider: "ollama",
		OllamaModel:   "nomic-embed-text",
	})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Store.ReadProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if rec.EmbedProvider != "ollama" || rec.EmbedModel != "nomic-embed-text" {
		t.Fatalf("embed fields: provider=%q model=%q", rec.EmbedProvider, rec.EmbedModel)
	}
	body, err := os.ReadFile(filepath.Join(s.Store.Root, "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "embed_provider: ollama") {
		t.Fatalf("yaml missing embed_provider line:\n%s", body)
	}
	if !strings.Contains(string(body), "embed_model: nomic-embed-text") {
		t.Fatalf("yaml missing embed_model line:\n%s", body)
	}
}

// OpenAI cfg gets the hardcoded text-embedding-3-small default since there's
// no separate OpenAIModel knob in config.
func TestCreateProject_OpenAIDefaultModel(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{
		EmbedProvider: "openai",
		OllamaModel:   "nomic-embed-text", // present but ignored for openai
	})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Store.ReadProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if rec.EmbedProvider != "openai" || rec.EmbedModel != "text-embedding-3-small" {
		t.Fatalf("embed fields: provider=%q model=%q", rec.EmbedProvider, rec.EmbedModel)
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

// CreateProject is intentionally auth-soft: it's the bootstrap escape valve,
// the one mutation that has to work pre-auth (every other mutation requires
// a session). With no session on the context, the project is created with
// no created_by attribution and the auto-commit is skipped.
func TestCreateProject_NoSessionSucceedsWithNilCreatedBy(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	p, err := s.CreateProject(context.Background(), "alpha", "Alpha", "", validSummary())
	if err != nil {
		t.Fatalf("CreateProject without session: %v", err)
	}
	if p.CreatedBy != nil {
		t.Errorf("CreatedBy should be nil with no session, got %+v", p.CreatedBy)
	}
	// Still mounted normally so subsequent reads work.
	got, err := s.GetProject(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("GetProject after no-session create: %v", err)
	}
	if got.Slug != "alpha" {
		t.Errorf("post-create slug: got %q want %q", got.Slug, "alpha")
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
	// Post-flatten a Store hosts at most one project, so we exercise the
	// "doesn't lazy-load" invariant with a single project. The next-ticket
	// multi-Store registry will cover the cross-project listing case.
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	beforeLen := s.Cache.Len()

	out, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 project, got %d", len(out))
	}
	if out[0].Slug != "alpha" {
		t.Fatalf("got slug %q, want alpha", out[0].Slug)
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
	disk, err := os.ReadFile(filepath.Join(s.Store.Root, "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(disk), newSummary) {
		t.Fatalf("disk summary did not pick up update: %q", string(disk[:50]))
	}
}

// UpdateProject writes embed_provider/embed_model when the input pointers are
// set and leaves them alone when nil — round-tripping the on-disk yaml.
func TestUpdateProject_WritesEmbedFields(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{
		EmbedProvider: "ollama",
		OllamaModel:   "nomic-embed-text",
	})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	newProvider := "openai"
	newModel := "text-embedding-3-small"
	if _, err := s.UpdateProject(ctx, "alpha", domain.UpdateProjectInput{
		EmbedProvider: &newProvider,
		EmbedModel:    &newModel,
	}); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	rec, err := s.Store.ReadProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if rec.EmbedProvider != newProvider || rec.EmbedModel != newModel {
		t.Fatalf("embed fields after update: provider=%q model=%q", rec.EmbedProvider, rec.EmbedModel)
	}

	// A no-op update doesn't clobber what's already there.
	newName := "Alpha v2"
	if _, err := s.UpdateProject(ctx, "alpha", domain.UpdateProjectInput{Name: &newName}); err != nil {
		t.Fatal(err)
	}
	rec, err = s.Store.ReadProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if rec.EmbedProvider != newProvider || rec.EmbedModel != newModel {
		t.Fatalf("embed fields lost after unrelated update: %+v", rec)
	}
}

// project.yaml fixtures from older binaries lack embed fields. omitempty plus
// re-read-then-write means UpdateProject backfills them only when the form
// supplies them; otherwise the record continues to load cleanly with empty
// strings.
func TestUpdateProject_LegacyYamlWithoutEmbedFieldsLoads(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	// Overwrite project.yaml with a legacy shape (no embed fields).
	yamlPath := filepath.Join(s.Store.Root, "project.yaml")
	rec, err := s.Store.ReadProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	rec.EmbedProvider = ""
	rec.EmbedModel = ""
	bytes, err := store.MarshalYAML(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yamlPath, bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bytes), "embed_provider") {
		t.Fatalf("fixture still mentions embed_provider:\n%s", bytes)
	}

	// Re-read works (omitempty leaves them as zero values).
	got, err := s.Store.ReadProject("alpha")
	if err != nil {
		t.Fatalf("re-read legacy yaml: %v", err)
	}
	if got.EmbedProvider != "" || got.EmbedModel != "" {
		t.Fatalf("expected zero embed fields, got %+v", got)
	}

	// Next UpdateProject backfills when supplied.
	prov, model := "ollama", "nomic-embed-text"
	if _, err := s.UpdateProject(ctx, "alpha", domain.UpdateProjectInput{
		EmbedProvider: &prov,
		EmbedModel:    &model,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Store.ReadProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.EmbedProvider != prov || got.EmbedModel != model {
		t.Fatalf("backfill failed: %+v", got)
	}
}

func TestDeleteProject_DeletesEvenWithActiveTickets(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	// Drop a ticket directly on disk in the `todo` column to prove that
	// project-level delete no longer cares about active tickets.
	tdir := filepath.Join(s.Store.Root, "tickets", "001-stub")
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

	if err := s.DeleteProject(ctx, "alpha"); err != nil {
		t.Fatalf("DeleteProject with active ticket: %v", err)
	}

	// Project siblings AND the active ticket dir are all gone.
	for _, rel := range []string{"project.yaml", "summary.md", "tickets"} {
		if _, err := os.Stat(filepath.Join(s.Store.Root, rel)); !os.IsNotExist(err) {
			t.Fatalf("expected %s gone, got err=%v", rel, err)
		}
	}
	if s.Cache.Len() != 0 {
		t.Fatalf("cache should be empty after delete, got %d", s.Cache.Len())
	}
}

func TestDeleteProject_HappyPath_RemovesViaStageOp(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	// Pre-delete: project files exist at the data-dir root (flat layout).
	yamlPath := filepath.Join(s.Store.Root, "project.yaml")
	if _, err := os.Stat(yamlPath); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteProject(ctx, "alpha"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	// Post-delete: project-owned siblings are gone, but the data dir itself
	// (which also holds agents/, .staging/, .lock) survives.
	for _, rel := range []string{"project.yaml", "summary.md"} {
		if _, err := os.Stat(filepath.Join(s.Store.Root, rel)); !os.IsNotExist(err) {
			t.Fatalf("expected %s gone, got err=%v", rel, err)
		}
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
