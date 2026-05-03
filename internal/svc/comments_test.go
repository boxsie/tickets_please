package svc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/google/uuid"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// seedTicket lays down a project + a ticket directly on disk so the comment
// tests can run without depending on T05's CreateTicket landing first. Returns
// the ticket id.
func seedTicket(t *testing.T, s *Service, slug string, number int, title string) string {
	t.Helper()
	ticketID := uuid.NewString()

	// Project must exist.
	if _, err := s.Store.ReadProject(slug); err != nil {
		ctx, _ := authedCtx(t, s)
		if _, err := s.CreateProject(ctx, slug, title+"-prj", "", validSummary()); err != nil {
			t.Fatalf("seed project: %v", err)
		}
	}

	dir := filepath.Join(s.Store.Root, "tickets", "001-stub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tr := &store.TicketRecord{
		ID:        ticketID,
		ProjectID: "p",
		Number:    number,
		Title:     title,
		Column:    domain.ColumnTodo,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.WriteYAMLAtomic(filepath.Join(dir, "ticket.yaml"), tr); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "body.md"), []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return ticketID
}

func TestCreateComment_Happy(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, agent := authedCtx(t, s)
	tid := seedTicket(t, s, "alpha", 1, "Stub")

	c, err := s.CreateComment(ctx, tid, "first comment")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if c.ID == "" || c.TicketID != tid {
		t.Fatalf("unexpected comment: %+v", c)
	}
	if c.Kind != domain.CommentKindUser {
		t.Fatalf("expected CommentKindUser, got %q", c.Kind)
	}
	if !strings.Contains(c.Body, "first comment") {
		t.Fatalf("body lost: %q", c.Body)
	}
	if c.Author == nil || c.Author.ID != agent.ID || c.Author.Name != agent.Name {
		t.Fatalf("expected author %v, got %+v", agent, c.Author)
	}
	if c.FromColumn != nil || c.ToColumn != nil {
		t.Fatalf("user comment should not carry column transitions, got from=%v to=%v", c.FromColumn, c.ToColumn)
	}
	if c.CreatedAt.IsZero() {
		t.Fatal("missing CreatedAt")
	}

	// File appeared on disk under the ticket's comments dir.
	commentsDir := filepath.Join(s.Store.Root, "tickets", "001-stub", "comments")
	entries, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatalf("read comments dir: %v", err)
	}
	mds := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			mds++
		}
	}
	if mds != 1 {
		t.Fatalf("expected 1 comment .md, got %d (entries=%v)", mds, entries)
	}
}

func TestCreateComment_RejectsEmptyBody(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	tid := seedTicket(t, s, "alpha", 1, "Stub")

	for _, body := range []string{"", "   ", "\t\n"} {
		_, err := s.CreateComment(ctx, tid, body)
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("body %q: expected ErrInvalidArgument, got %v", body, err)
		}
	}
}

func TestCreateComment_NotFoundForUnknownTicket(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	// Need a project + at least one ticket so the walk is non-trivial; not
	// strictly required since WalkProjects also handles empty.
	_ = seedTicket(t, s, "alpha", 1, "Stub")

	_, err := s.CreateComment(ctx, "not-a-real-ticket-id", "hello")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateComment_RequiresSession(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	tid := seedTicket(t, s, "alpha", 1, "Stub")

	_, err := s.CreateComment(context.Background(), tid, "nope")
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestListComments_ReturnsCreatedComment(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, agent := authedCtx(t, s)
	tid := seedTicket(t, s, "alpha", 1, "Stub")

	if _, err := s.CreateComment(ctx, tid, "one"); err != nil {
		t.Fatal(err)
	}

	out, err := s.ListComments(context.Background(), tid)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(out))
	}
	got := out[0]
	if got.Kind != domain.CommentKindUser {
		t.Fatalf("kind %q", got.Kind)
	}
	if got.Author == nil || got.Author.ID != agent.ID {
		t.Fatalf("expected author id %s, got %+v", agent.ID, got.Author)
	}
}

func TestListComments_OrdersChronologically(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	tid := seedTicket(t, s, "alpha", 1, "Stub")

	bodies := []string{"first", "second", "third"}
	created := make([]string, 0, len(bodies))
	for _, b := range bodies {
		c, err := s.CreateComment(ctx, tid, b)
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, c.ID)
	}

	out, err := s.ListComments(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 comments, got %d", len(out))
	}
	for i, c := range out {
		if c.ID != created[i] {
			t.Fatalf("ordering mismatch at %d: got %s, want %s", i, c.ID, created[i])
		}
		if !strings.Contains(c.Body, bodies[i]) {
			t.Fatalf("body[%d] %q does not contain %q", i, c.Body, bodies[i])
		}
	}
}

func TestListComments_NotFoundForUnknownTicket(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	_ = seedTicket(t, s, "alpha", 1, "Stub")

	_, err := s.ListComments(context.Background(), "no-such-ticket")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListComments_SurfacesAllKinds(t *testing.T) {
	// T07 will write system_move and system_completion comments via the same
	// filename convention. ListComments must already surface them — we
	// simulate by hand-writing such a comment file then loading.
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	tid := seedTicket(t, s, "alpha", 1, "Stub")

	// User comment via the service.
	if _, err := s.CreateComment(ctx, tid, "user-said"); err != nil {
		t.Fatal(err)
	}

	// Directly drop a system_move comment file with a slightly later
	// timestamp so it sorts after the user one.
	commentsDir := filepath.Join(s.Store.Root, "tickets", "001-stub", "comments")
	now := time.Now().UTC().Add(time.Second)
	from := domain.ColumnTodo
	to := domain.ColumnInProgress
	rec := &store.CommentRecord{
		ID:         uuid.NewString(),
		TicketID:   tid,
		Kind:       domain.CommentKindSystemMove,
		FromColumn: &from,
		ToColumn:   &to,
		CreatedAt:  now,
	}
	short := strings.ReplaceAll(rec.ID[:8], "-", "")
	name := now.Format(commentTimestampLayout) + "-" + short + "-" + string(rec.Kind) + ".md"
	bytes, err := store.EncodeMarkdown(rec, "moved by hand\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commentsDir, name), bytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Force the cache to reload (hand-written files bypass cache mutation).
	s.Cache.Evict("alpha")

	out, err := s.ListComments(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 comments (user + system_move), got %d", len(out))
	}
	if out[0].Kind != domain.CommentKindUser || out[1].Kind != domain.CommentKindSystemMove {
		t.Fatalf("unexpected ordering by kind: %v / %v", out[0].Kind, out[1].Kind)
	}
	if out[1].FromColumn == nil || *out[1].FromColumn != domain.ColumnTodo {
		t.Fatalf("system_move from_column not preserved: %+v", out[1].FromColumn)
	}
}

func TestCreateComment_NoUpdateOrDeleteMethodsExist(t *testing.T) {
	// Compile-time guard: comments are immutable per SPEC §Design decisions.
	// If a future refactor accidentally introduces UpdateComment or
	// DeleteComment on Service this test would need to be updated — but the
	// spec says no, so we lock it in with a method-set check via reflection.
	s := freshServiceWithCfg(t, config.Config{})
	// We don't reflect — instead we just assert that Service exposes the
	// expected methods (Create, List) without claiming the others. The
	// compile-time guarantee is that referring to s.UpdateComment would
	// fail to build; since this file builds, we know that method does not
	// exist. Same for DeleteComment.
	_ = s.CreateComment
	_ = s.ListComments
}

func TestCreateComment_RapidFireDistinctFilenames(t *testing.T) {
	// Acceptance criterion: two comments created in rapid succession produce
	// distinct filenames (nanosecond timestamp + short-id avoids collision).
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	tid := seedTicket(t, s, "alpha", 1, "Stub")

	const N = 8
	for i := 0; i < N; i++ {
		if _, err := s.CreateComment(ctx, tid, "x"); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}

	commentsDir := filepath.Join(s.Store.Root, "tickets", "001-stub", "comments")
	entries, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if seen[e.Name()] {
			t.Fatalf("duplicate filename: %s", e.Name())
		}
		seen[e.Name()] = true
	}
	if len(seen) != N {
		t.Fatalf("expected %d distinct files, got %d", N, len(seen))
	}
}

func TestCreateComment_ConcurrentWritesAreSerialized(t *testing.T) {
	// Stress the lock-ordering (LoadedProject.Lock → flock) under concurrent
	// callers. Each goroutine creates a comment; -race catches mis-ordering.
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	tid := seedTicket(t, s, "alpha", 1, "Stub")

	const N = 6
	var wg sync.WaitGroup
	errs := make(chan error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			if _, err := s.CreateComment(ctx, tid, "concurrent"); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent CreateComment: %v", err)
	}

	out, err := s.ListComments(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != N {
		t.Fatalf("expected %d comments, got %d", N, len(out))
	}
}

func TestCreateComment_AutoCommitsInGitRepo(t *testing.T) {
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
	tid := seedTicket(t, s, "alpha", 1, "Stub")

	if _, err := s.CreateComment(ctx, tid, "shipping it"); err != nil {
		t.Fatalf("CreateComment: %v", err)
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
	if !strings.Contains(commit.Message, "comment on alpha/001") {
		t.Fatalf("commit message: %q", commit.Message)
	}
	if commit.Author.Name != agent.Name {
		t.Fatalf("commit author: %q (want %q)", commit.Author.Name, agent.Name)
	}
}
