package svc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/google/uuid"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// freshServiceWithProject is the standard test setup: register an agent,
// create one project, and return both. Most ticket tests start from here.
func freshServiceWithProject(t *testing.T) (*Service, context.Context, *domain.Agent, string) {
	t.Helper()
	s := freshServiceWithCfg(t, config.Config{})
	ctx, agent := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return s, ctx, agent, "alpha"
}

func TestCreateTicket_HappyPath(t *testing.T) {
	s, ctx, agent, slug := freshServiceWithProject(t)

	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug,
		Title:           "Implement login flow",
		Body:            "Wire OAuth + session cookies.",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if tk.Column != domain.ColumnTodo {
		t.Fatalf("expected column todo, got %s", tk.Column)
	}
	if tk.ID == "" {
		t.Fatal("missing ID")
	}
	if tk.CreatedBy == nil || tk.CreatedBy.ID != agent.ID {
		t.Fatalf("expected created_by attribution: %+v", tk.CreatedBy)
	}

	// Directory layout: projects/alpha/tickets/001-implement-login-flow/.
	want := filepath.Join(s.Store.Root, "tickets", "001-implement-login-flow")
	for _, f := range []string{"ticket.yaml", "body.md"} {
		if _, err := os.Stat(filepath.Join(want, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
}

func TestCreateTicket_RejectsEmptyTitle(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	_, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "   "})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestCreateTicket_RequiresSession(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateTicket(context.Background(), domain.CreateTicketInput{ProjectIDOrSlug: "alpha", Title: "no session"})
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestCreateTicket_RejectsNegativeWave(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	_, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "x", Wave: -1,
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestCreateTicket_NumbersAreSequential(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	for i := 1; i <= 3; i++ {
		_, err := s.CreateTicket(ctx, domain.CreateTicketInput{
			ProjectIDOrSlug: slug,
			Title:           "Ticket " + string(rune('A'+i-1)),
		})
		if err != nil {
			t.Fatalf("CreateTicket %d: %v", i, err)
		}
	}

	want := []string{"001-ticket-a", "002-ticket-b", "003-ticket-c"}
	for _, dn := range want {
		path := filepath.Join(s.Store.Root, "tickets", dn)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing dir %s: %v", dn, err)
		}
	}
}

func TestCreateTicket_RejectsCrossProjectDeps(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	_, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "x",
		DependsOn: []string{uuid.NewString()},
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for unknown dep, got %v", err)
	}
}

func TestCreateTicket_DependsOnSameProjectAccepted(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	first, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "second",
		DependsOn: []string{first.ID},
	})
	if err != nil {
		t.Fatalf("CreateTicket with intra-project dep: %v", err)
	}
	if len(second.BlockedBy) != 1 || second.BlockedBy[0] != first.ID {
		t.Fatalf("expected BlockedBy=[%s], got %+v", first.ID, second.BlockedBy)
	}
}

func TestCreateTicket_PhaseRequiresExistingPhase(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	bad := "phase-does-not-exist"
	_, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "x", PhaseIDOrSlug: &bad,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetTicket_HappyPath(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "x", Body: "body content",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTicket(ctx, tk.ID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.Title != "x" || !strings.Contains(got.Body, "body content") {
		t.Fatalf("unexpected ticket: %+v", got)
	}
}

func TestGetTicket_NotFound(t *testing.T) {
	s, ctx, _, _ := freshServiceWithProject(t)
	_, err := s.GetTicket(ctx, "no-such-id")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListTickets_FiltersByColumn(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	for i := 0; i < 3; i++ {
		if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
			ProjectIDOrSlug: slug, Title: "t" + string(rune('1'+i)),
		}); err != nil {
			t.Fatal(err)
		}
	}

	col := domain.ColumnTodo
	out, _, err := s.ListTickets(ctx, domain.ListTicketsInput{
		ProjectIDOrSlug: slug, Column: &col,
	})
	if err != nil {
		t.Fatalf("ListTickets: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 todo tickets, got %d", len(out))
	}

	// Filter on a column that has zero matches.
	col = domain.ColumnDone
	out, _, err = s.ListTickets(ctx, domain.ListTicketsInput{
		ProjectIDOrSlug: slug, Column: &col,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 done tickets, got %d", len(out))
	}
}

func TestListTickets_Pagination(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	for i := 0; i < 5; i++ {
		if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
			ProjectIDOrSlug: slug, Title: "t" + string(rune('1'+i)),
		}); err != nil {
			t.Fatal(err)
		}
		// Tiny gap so CreatedAt is distinct (timestamp-based cursor needs
		// it for unambiguous ordering).
		time.Sleep(2 * time.Millisecond)
	}

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		pages++
		out, next, err := s.ListTickets(ctx, domain.ListTicketsInput{
			ProjectIDOrSlug: slug, Limit: 2, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, tk := range out {
			if seen[tk.ID] {
				t.Fatalf("duplicate id across pages: %s", tk.ID)
			}
			seen[tk.ID] = true
		}
		if next == "" {
			break
		}
		cursor = next
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != 5 {
		t.Fatalf("expected 5 unique tickets across pages, got %d", len(seen))
	}
	if pages != 3 {
		t.Fatalf("expected 3 pages for limit=2 over 5 tickets, got %d", pages)
	}
}

func TestListTickets_ReadyOnlyHidesBlocked(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	dep, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "dep"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "blocked", DependsOn: []string{dep.ID},
	}); err != nil {
		t.Fatal(err)
	}

	out, _, err := s.ListTickets(ctx, domain.ListTicketsInput{
		ProjectIDOrSlug: slug, ReadyOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != dep.ID {
		t.Fatalf("expected only dep to be ready, got %+v", out)
	}
}

func TestListTickets_BadCursor(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	_, _, err := s.ListTickets(ctx, domain.ListTicketsInput{
		ProjectIDOrSlug: slug, Cursor: "garbage!!!",
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestListTickets_WaveOrderingZeroLast(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	mk := func(title string, wave int) *domain.Ticket {
		t.Helper()
		tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
			ProjectIDOrSlug: slug, Title: title, Wave: wave,
		})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
		return tk
	}
	w0 := mk("zero", 0)
	w2 := mk("two", 2)
	w1 := mk("one", 1)

	out, _, err := s.ListTickets(ctx, domain.ListTicketsInput{ProjectIDOrSlug: slug})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3, got %d", len(out))
	}
	want := []string{w1.ID, w2.ID, w0.ID}
	for i, id := range want {
		if out[i].ID != id {
			t.Fatalf("position %d: want %s, got %s", i, id, out[i].ID)
		}
	}
}

func TestUpdateTicket_TitleOnly_KeepsBody(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "old title", Body: "important body",
	})
	if err != nil {
		t.Fatal(err)
	}

	newTitle := "new title"
	got, err := s.UpdateTicket(ctx, tk.ID, domain.UpdateTicketInput{Title: &newTitle})
	if err != nil {
		t.Fatalf("UpdateTicket: %v", err)
	}
	if got.Title != newTitle {
		t.Fatalf("title not updated: %q", got.Title)
	}
	if !strings.Contains(got.Body, "important body") {
		t.Fatalf("body got blanked: %q", got.Body)
	}

	// Disk: body.md still has the original content.
	dir := filepath.Join(s.Store.Root, "tickets", "001-old-title")
	disk, err := os.ReadFile(filepath.Join(dir, "body.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(disk), "important body") {
		t.Fatalf("disk body lost original content: %q", string(disk))
	}
}

func TestUpdateTicket_ReplacesDependencyLists(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	upstream, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "upstream"})
	if err != nil {
		t.Fatal(err)
	}
	parallel, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "parallel"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "child"})
	if err != nil {
		t.Fatal(err)
	}

	deps := []string{upstream.ID}
	parallelWith := []string{parallel.ID}
	got, err := s.UpdateTicket(ctx, child.ID, domain.UpdateTicketInput{
		DependsOn:          &deps,
		ParallelizableWith: &parallelWith,
	})
	if err != nil {
		t.Fatalf("UpdateTicket set deps: %v", err)
	}
	if !slices.Equal(got.DependsOn, deps) {
		t.Fatalf("DependsOn = %v, want %v", got.DependsOn, deps)
	}
	if !slices.Equal(got.ParallelizableWith, parallelWith) {
		t.Fatalf("ParallelizableWith = %v, want %v", got.ParallelizableWith, parallelWith)
	}
	if !slices.Equal(got.BlockedBy, deps) {
		t.Fatalf("BlockedBy = %v, want %v", got.BlockedBy, deps)
	}

	var rec store.TicketRecord
	if err := store.ReadYAML(filepath.Join(s.Store.Root, "tickets", "003-child", "ticket.yaml"), &rec); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.DependsOn, deps) {
		t.Fatalf("disk DependsOn = %v, want %v", rec.DependsOn, deps)
	}
	if !slices.Equal(rec.ParallelizableWith, parallelWith) {
		t.Fatalf("disk ParallelizableWith = %v, want %v", rec.ParallelizableWith, parallelWith)
	}

	empty := []string{}
	got, err = s.UpdateTicket(ctx, child.ID, domain.UpdateTicketInput{
		DependsOn:          &empty,
		ParallelizableWith: &empty,
	})
	if err != nil {
		t.Fatalf("UpdateTicket clear deps: %v", err)
	}
	if len(got.DependsOn) != 0 || len(got.ParallelizableWith) != 0 || len(got.BlockedBy) != 0 {
		t.Fatalf("deps not cleared: depends=%v parallel=%v blocked=%v", got.DependsOn, got.ParallelizableWith, got.BlockedBy)
	}
}

func TestUpdateTicket_DependencyValidation(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	a, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "b"})
	if err != nil {
		t.Fatal(err)
	}

	self := []string{a.ID}
	if _, err := s.UpdateTicket(ctx, a.ID, domain.UpdateTicketInput{DependsOn: &self}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("self dependency: expected ErrInvalidArgument, got %v", err)
	}
	missing := []string{uuid.NewString()}
	if _, err := s.UpdateTicket(ctx, a.ID, domain.UpdateTicketInput{DependsOn: &missing}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("missing dependency: expected ErrInvalidArgument, got %v", err)
	}

	aDependsOnB := []string{b.ID}
	if _, err := s.UpdateTicket(ctx, a.ID, domain.UpdateTicketInput{DependsOn: &aDependsOnB}); err != nil {
		t.Fatalf("seed dependency: %v", err)
	}
	bDependsOnA := []string{a.ID}
	if _, err := s.UpdateTicket(ctx, b.ID, domain.UpdateTicketInput{DependsOn: &bDependsOnA}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("cycle dependency: expected ErrInvalidArgument, got %v", err)
	}
}

func TestUpdateTicket_RejectsBlankTitle(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: slug, Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	blank := "   "
	if _, err := s.UpdateTicket(ctx, tk.ID, domain.UpdateTicketInput{Title: &blank}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestUpdateTicket_NotFound(t *testing.T) {
	s, ctx, _, _ := freshServiceWithProject(t)
	new := "x"
	_, err := s.UpdateTicket(ctx, uuid.NewString(), domain.UpdateTicketInput{Title: &new})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateTicket_AutoCommitsInGitRepo(t *testing.T) {
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
		t.Fatal(err)
	}

	if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "Ship it",
	}); err != nil {
		t.Fatalf("CreateTicket: %v", err)
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
	if !strings.Contains(commit.Message, "create ticket alpha/001") {
		t.Fatalf("unexpected commit message: %q", commit.Message)
	}
	if commit.Author.Name != agent.Name {
		t.Fatalf("commit author: %q (want %q)", commit.Author.Name, agent.Name)
	}
}

func TestUpdateTicket_AutoCommitsInGitRepo(t *testing.T) {
	repoDir := t.TempDir()
	if _, err := git.PlainInit(repoDir, false); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		DataDir:    filepath.Join(repoDir, ".tickets_please"),
		AutoCommit: true,
	}
	s := freshServiceWithCfg(t, cfg)
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: "alpha", Title: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	newTitle := "Hello v2"
	if _, err := s.UpdateTicket(ctx, tk.ID, domain.UpdateTicketInput{Title: &newTitle}); err != nil {
		t.Fatal(err)
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
	if !strings.Contains(commit.Message, "update ticket alpha/001") {
		t.Fatalf("unexpected commit message: %q", commit.Message)
	}
}

func TestCreateTicket_PhasedRoutesToPhaseDir(t *testing.T) {
	s, ctx, agent, slug := freshServiceWithProject(t)

	// Hand-write a phase on disk + into the cache so we can land a phased
	// ticket without needing T16's CreatePhase. Also exercises the phase
	// dir-name derivation path.
	phaseID := uuid.NewString()
	phaseDir := filepath.Join(s.Store.Root, "phases", "001-discovery")
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pr := &store.PhaseRecord{
		ID: phaseID, ProjectID: "x", Slug: "discovery", Number: 1,
		Name: "Discovery", CreatedByAgentID: &agent.ID,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.WriteYAMLAtomic(filepath.Join(phaseDir, "phase.yaml"), pr); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, "summary.md"), []byte("phase summary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force a cache reload so the phase populates.
	s.Cache.Evict(slug)

	phaseSlug := "discovery"
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "phased work", PhaseIDOrSlug: &phaseSlug,
	})
	if err != nil {
		t.Fatalf("CreateTicket phased: %v", err)
	}
	if tk.PhaseID == nil || *tk.PhaseID != phaseID {
		t.Fatalf("expected PhaseID=%s, got %v", phaseID, tk.PhaseID)
	}

	want := filepath.Join(phaseDir, "tickets", "001-phased-work", "ticket.yaml")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected phased ticket at %s: %v", want, err)
	}
}

func TestListTickets_PhaseLessSentinel(t *testing.T) {
	s, ctx, agent, slug := freshServiceWithProject(t)

	// Create a phase + a phased ticket, plus a phase-less ticket.
	phaseID := uuid.NewString()
	phaseDir := filepath.Join(s.Store.Root, "phases", "001-discovery")
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pr := &store.PhaseRecord{
		ID: phaseID, ProjectID: "x", Slug: "discovery", Number: 1,
		Name: "Discovery", CreatedByAgentID: &agent.ID,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.WriteYAMLAtomic(filepath.Join(phaseDir, "phase.yaml"), pr); err != nil {
		t.Fatal(err)
	}
	s.Cache.Evict(slug)

	phaseSlug := "discovery"
	if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "phased", PhaseIDOrSlug: &phaseSlug,
	}); err != nil {
		t.Fatal(err)
	}
	pl, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "phaseless",
	})
	if err != nil {
		t.Fatal(err)
	}

	dash := "-"
	out, _, err := s.ListTickets(ctx, domain.ListTicketsInput{
		ProjectIDOrSlug: slug, PhaseIDOrSlug: &dash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != pl.ID {
		t.Fatalf("expected only phaseless ticket via sentinel, got %+v", out)
	}
}

func TestDeleteTicket_HappyPath(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "Throwaway",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	dirRel, _, err := s.findTicketDir(s.Store, slug, tk.ID)
	if err != nil {
		t.Fatalf("findTicketDir: %v", err)
	}
	dirAbs := filepath.Join(s.Store.Root, dirRel)

	if err := s.DeleteTicket(ctx, tk.ID); err != nil {
		t.Fatalf("DeleteTicket: %v", err)
	}

	if _, err := os.Stat(dirAbs); !os.IsNotExist(err) {
		t.Fatalf("expected ticket dir gone, got err=%v", err)
	}
	if _, err := s.GetTicket(ctx, tk.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetTicket after delete: want ErrNotFound, got %v", err)
	}
}

func TestDeleteTicket_RefusesDoneTickets(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "Will complete",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "starting"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnTesting, "qa"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTicket(ctx, tk.ID, "ran the suite", "shipped a thing", "delete is gated on done"); err != nil {
		t.Fatal(err)
	}

	err = s.DeleteTicket(ctx, tk.ID)
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition, got %v", err)
	}
	if !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("error should mention completion is frozen, got %q", err)
	}
	// Ticket still exists.
	if _, err := s.GetTicket(ctx, tk.ID); err != nil {
		t.Fatalf("GetTicket after refused delete: %v", err)
	}
}

func TestDeleteTicket_CascadesDependentRefs(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	a, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "Foundation",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "Built atop foundation", DependsOn: []string{a.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "Sibling work", ParallelizableWith: []string{a.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteTicket(ctx, a.ID); err != nil {
		t.Fatalf("DeleteTicket(a): %v", err)
	}

	// Doomed ticket gone.
	if _, err := s.GetTicket(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a should be gone, got %v", err)
	}

	// b's DependsOn no longer includes a.
	gotB, err := s.GetTicket(ctx, b.ID)
	if err != nil {
		t.Fatalf("GetTicket(b): %v", err)
	}
	for _, dep := range gotB.DependsOn {
		if dep == a.ID {
			t.Fatalf("b.DependsOn still contains a (%s) after cascade: %v", a.ID, gotB.DependsOn)
		}
	}
	if len(gotB.BlockedBy) != 0 {
		t.Fatalf("b.BlockedBy should be empty after a was removed: %v", gotB.BlockedBy)
	}

	// c's ParallelizableWith no longer includes a.
	gotC, err := s.GetTicket(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetTicket(c): %v", err)
	}
	for _, par := range gotC.ParallelizableWith {
		if par == a.ID {
			t.Fatalf("c.ParallelizableWith still contains a (%s) after cascade: %v", a.ID, gotC.ParallelizableWith)
		}
	}

	// On-disk yaml for b matches the in-memory result (cascade was persisted,
	// not just cached).
	bDirRel, _, err := s.findTicketDir(s.Store, slug, b.ID)
	if err != nil {
		t.Fatalf("findTicketDir(b): %v", err)
	}
	rec := &store.TicketRecord{}
	if err := store.ReadYAML(filepath.Join(s.Store.Root, bDirRel, "ticket.yaml"), rec); err != nil {
		t.Fatalf("read b yaml: %v", err)
	}
	for _, dep := range rec.DependsOn {
		if dep == a.ID {
			t.Fatalf("on-disk b.DependsOn still contains a: %v", rec.DependsOn)
		}
	}
}

func TestDeleteTicket_RequiresSession(t *testing.T) {
	s, ctx, _, slug := freshServiceWithProject(t)
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: "Anon delete",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTicket(context.Background(), tk.ID); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}
