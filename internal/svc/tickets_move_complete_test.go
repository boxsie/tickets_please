package svc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// moveCompleteScenario builds a service + project + one fresh todo ticket so
// the move/complete tests can branch from a common starting state.
func moveCompleteScenario(t *testing.T, cfg config.Config) (*Service, context.Context, *domain.Agent, *domain.Ticket) {
	t.Helper()
	s := freshServiceWithCfg(t, cfg)
	ctx, agent := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha",
		Title:           "Implement feature",
		Body:            "describe the work",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	return s, ctx, agent, tk
}

func TestMoveTicket_RejectsEmptyComment(t *testing.T) {
	s, ctx, _, tk := moveCompleteScenario(t, config.Config{})
	for _, body := range []string{"", "   ", "\t\n"} {
		_, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, body)
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("body %q: expected ErrInvalidArgument, got %v", body, err)
		}
		if !strings.Contains(err.Error(), "comment") {
			t.Fatalf("error must name the field: %v", err)
		}
	}
}

func TestMoveTicket_RejectsDoneTarget(t *testing.T) {
	s, ctx, _, tk := moveCompleteScenario(t, config.Config{})
	_, err := s.MoveTicket(ctx, tk.ID, domain.ColumnDone, "starting work")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
	if !strings.Contains(err.Error(), "CompleteTicket") {
		t.Fatalf("expected error message to mention CompleteTicket, got %q", err.Error())
	}
}

func TestMoveTicket_RejectsMoveFromDone(t *testing.T) {
	s, ctx, _, tk := moveCompleteScenario(t, config.Config{})
	if _, err := s.CompleteTicket(ctx, tk.ID,
		"ran the suite locally", "shipped feature X", "watch out for race Y"); err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}
	_, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "reopen please")
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition, got %v", err)
	}
}

func TestMoveTicket_HappyPath_UpdatesYAMLAndAddsSystemMove(t *testing.T) {
	s, ctx, agent, tk := moveCompleteScenario(t, config.Config{})

	got, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "picking this up")
	if err != nil {
		t.Fatalf("MoveTicket: %v", err)
	}
	if got.Column != domain.ColumnInProgress {
		t.Fatalf("column not updated on returned ticket: %s", got.Column)
	}

	// Disk: ticket.yaml column flipped.
	dir := filepath.Join(s.Store.Root, "tickets", "001-implement-feature")
	yamlBytes, err := os.ReadFile(filepath.Join(dir, "ticket.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(yamlBytes), "column: in_progress") {
		t.Fatalf("ticket.yaml did not flip column: %s", yamlBytes)
	}

	// Disk: a new system_move.md comment file.
	commentsDir := filepath.Join(dir, "comments")
	entries, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatal(err)
	}
	var sysMoveName string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-system_move.md") {
			sysMoveName = e.Name()
			break
		}
	}
	if sysMoveName == "" {
		t.Fatalf("no system_move comment file in %v", entries)
	}
	body, err := os.ReadFile(filepath.Join(commentsDir, sysMoveName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "from_column: todo") {
		t.Fatalf("missing from_column: %s", body)
	}
	if !strings.Contains(string(body), "to_column: in_progress") {
		t.Fatalf("missing to_column: %s", body)
	}
	if !strings.Contains(string(body), "picking this up") {
		t.Fatalf("body lost: %s", body)
	}

	// ListComments interleaves the system_move.
	comments, err := s.ListComments(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	c := comments[0]
	if c.Kind != domain.CommentKindSystemMove {
		t.Fatalf("kind=%q", c.Kind)
	}
	if c.FromColumn == nil || *c.FromColumn != domain.ColumnTodo {
		t.Fatalf("from_column wrong: %+v", c.FromColumn)
	}
	if c.ToColumn == nil || *c.ToColumn != domain.ColumnInProgress {
		t.Fatalf("to_column wrong: %+v", c.ToColumn)
	}
	if c.Author == nil || c.Author.ID != agent.ID {
		t.Fatalf("expected author=%s, got %+v", agent.ID, c.Author)
	}
}

func TestMoveTicket_UnmetDeps_EnforcementOff_AnnotatesAndProceeds(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{EnforceDependencies: false})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	dep, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: "alpha", Title: "Dep"})
	if err != nil {
		t.Fatal(err)
	}
	follower, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "Follower",
		DependsOn: []string{dep.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.MoveTicket(ctx, follower.ID, domain.ColumnInProgress, "starting anyway")
	if err != nil {
		t.Fatalf("MoveTicket should succeed with enforcement off: %v", err)
	}
	if got.Column != domain.ColumnInProgress {
		t.Fatalf("expected in_progress, got %s", got.Column)
	}

	comments, err := s.ListComments(ctx, follower.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sysMove *domain.Comment
	for _, c := range comments {
		if c.Kind == domain.CommentKindSystemMove {
			sysMove = c
		}
	}
	if sysMove == nil {
		t.Fatalf("no system_move comment found; got %d comments", len(comments))
	}
	if !strings.Contains(sysMove.Body, "moved with unmet deps:") {
		t.Fatalf("expected unmet-deps annotation, got body: %q", sysMove.Body)
	}
	if !strings.Contains(sysMove.Body, "starting anyway") {
		t.Fatalf("expected user comment in body, got: %q", sysMove.Body)
	}
}

func TestMoveTicket_UnmetDeps_EnforcementOn_Blocks(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{EnforceDependencies: true})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	dep, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: "alpha", Title: "Dep"})
	if err != nil {
		t.Fatal(err)
	}
	follower, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "Follower",
		DependsOn: []string{dep.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.MoveTicket(ctx, follower.ID, domain.ColumnInProgress, "lets go")
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition, got %v", err)
	}
	if !strings.Contains(err.Error(), dep.ID) {
		t.Fatalf("expected error to list unmet dep id %s, got: %v", dep.ID, err)
	}
}

func TestMoveTicket_NonInProgressTransitionSkipsDepCheck(t *testing.T) {
	// Moving to `testing` (not `in_progress`) must NOT fire the dep check
	// even with enforcement on.
	s := freshServiceWithCfg(t, config.Config{EnforceDependencies: true})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	dep, err := s.CreateTicket(ctx, domain.CreateTicketInput{ProjectIDOrSlug: "alpha", Title: "Dep"})
	if err != nil {
		t.Fatal(err)
	}
	follower, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "Follower",
		DependsOn: []string{dep.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.MoveTicket(ctx, follower.ID, domain.ColumnTesting, "skip ahead to testing")
	if err != nil {
		t.Fatalf("MoveTicket to testing should bypass dep check: %v", err)
	}
	if got.Column != domain.ColumnTesting {
		t.Fatalf("expected testing, got %s", got.Column)
	}
}

func TestMoveTicket_NotFound(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	_, err := s.MoveTicket(ctx, "no-such-id", domain.ColumnInProgress, "go")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMoveTicket_RequiresSession(t *testing.T) {
	s, _, _, tk := moveCompleteScenario(t, config.Config{})
	_, err := s.MoveTicket(context.Background(), tk.ID, domain.ColumnInProgress, "go")
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestCompleteTicket_RejectsShortFields(t *testing.T) {
	s, ctx, _, tk := moveCompleteScenario(t, config.Config{})
	cases := []struct {
		te, ws, ln string
	}{
		{".", "decent work summary here", "decent learnings here"},
		{"decent testing here yes", ".", "decent learnings here"},
		{"decent testing here yes", "decent work summary", "."},
	}
	for i, c := range cases {
		_, err := s.CompleteTicket(ctx, tk.ID, c.te, c.ws, c.ln)
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("case %d: expected ErrInvalidArgument, got %v", i, err)
		}
	}
}

func TestCompleteTicket_RejectsAlreadyDone(t *testing.T) {
	s, ctx, _, tk := moveCompleteScenario(t, config.Config{})
	if _, err := s.CompleteTicket(ctx, tk.ID,
		"ran the test suite", "implemented the thing", "watch the off-by-one"); err != nil {
		t.Fatalf("first CompleteTicket: %v", err)
	}
	_, err := s.CompleteTicket(ctx, tk.ID,
		"ran the test suite", "implemented the thing", "watch the off-by-one")
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition, got %v", err)
	}
}

func TestCompleteTicket_HappyPath_WritesAllFiles(t *testing.T) {
	s, ctx, agent, tk := moveCompleteScenario(t, config.Config{})

	te := "ran ./... and the integration suite locally"
	ws := "wired the new MoveTicket + CompleteTicket flows"
	ln := "system_move and system_completion comments share the filename pattern"
	got, err := s.CompleteTicket(ctx, tk.ID, te, ws, ln)
	if err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}
	if got.Column != domain.ColumnDone {
		t.Fatalf("column=%s, want done", got.Column)
	}
	if got.CompletedAt == nil || got.CompletedAt.IsZero() {
		t.Fatalf("CompletedAt not set: %+v", got.CompletedAt)
	}
	if got.CompletedBy == nil || got.CompletedBy.ID != agent.ID {
		t.Fatalf("CompletedBy=%v, want id=%s", got.CompletedBy, agent.ID)
	}
	if got.TestingEvidence == nil || *got.TestingEvidence != te {
		t.Fatalf("TestingEvidence not populated: %+v", got.TestingEvidence)
	}
	if got.WorkSummary == nil || *got.WorkSummary != ws {
		t.Fatalf("WorkSummary not populated: %+v", got.WorkSummary)
	}
	if got.Learnings == nil || *got.Learnings != ln {
		t.Fatalf("Learnings not populated: %+v", got.Learnings)
	}

	dir := filepath.Join(s.Store.Root, "tickets", "001-implement-feature")

	// completion.md exists with the three sections.
	completion, err := os.ReadFile(filepath.Join(dir, "completion.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"## Testing evidence", "## Work summary", "## Learnings"} {
		if !strings.Contains(string(completion), h) {
			t.Fatalf("completion.md missing section %q: %s", h, completion)
		}
	}
	if !strings.Contains(string(completion), te) ||
		!strings.Contains(string(completion), ws) ||
		!strings.Contains(string(completion), ln) {
		t.Fatalf("completion.md missing payload: %s", completion)
	}

	// ticket.yaml has done + completed_by.
	yamlBytes, err := os.ReadFile(filepath.Join(dir, "ticket.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(yamlBytes), "column: done") {
		t.Fatalf("ticket.yaml column not done: %s", yamlBytes)
	}
	if !strings.Contains(string(yamlBytes), "completed_by: "+agent.ID) {
		t.Fatalf("ticket.yaml completed_by missing: %s", yamlBytes)
	}

	// system_completion comment file present.
	commentsDir := filepath.Join(dir, "comments")
	entries, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatal(err)
	}
	var sysName string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-system_completion.md") {
			sysName = e.Name()
		}
	}
	if sysName == "" {
		t.Fatalf("no system_completion comment file: %v", entries)
	}
	body, err := os.ReadFile(filepath.Join(commentsDir, sysName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "kind: system_completion") {
		t.Fatalf("expected kind=system_completion frontmatter: %s", body)
	}
	if !strings.Contains(string(body), "✅ Ticket completed.") {
		t.Fatalf("expected completion banner in body: %s", body)
	}
	for _, w := range []string{te, ws, ln} {
		if !strings.Contains(string(body), w) {
			t.Fatalf("system_completion missing %q: %s", w, body)
		}
	}

	// ListComments shows the system_completion entry.
	comments, err := s.ListComments(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range comments {
		if c.Kind == domain.CommentKindSystemCompletion {
			found = true
			if c.Author == nil || c.Author.ID != agent.ID {
				t.Fatalf("expected author=%s, got %+v", agent.ID, c.Author)
			}
		}
	}
	if !found {
		t.Fatalf("system_completion not in ListComments: %+v", comments)
	}
}

func TestCompleteTicket_RequiresSession(t *testing.T) {
	s, _, _, tk := moveCompleteScenario(t, config.Config{})
	_, err := s.CompleteTicket(context.Background(), tk.ID,
		"ran the suite", "shipped it", "lessons learned")
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestMoveTicket_AutoCommitsSingleCommit(t *testing.T) {
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
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "Implement feature",
	})
	if err != nil {
		t.Fatal(err)
	}

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	preCount := countCommits(t, repo)

	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "starting"); err != nil {
		t.Fatalf("MoveTicket: %v", err)
	}

	postCount := countCommits(t, repo)
	if postCount-preCount != 1 {
		t.Fatalf("expected exactly 1 new commit for MoveTicket, got %d (was %d)", postCount-preCount, preCount)
	}

	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(commit.Message, "move ticket alpha/001 todo→in_progress") {
		t.Fatalf("unexpected commit message: %q", commit.Message)
	}
}

func TestCompleteTicket_AutoCommitsSingleCommit(t *testing.T) {
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
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "Implement feature",
	})
	if err != nil {
		t.Fatal(err)
	}

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	preCount := countCommits(t, repo)

	if _, err := s.CompleteTicket(ctx, tk.ID,
		"ran the local suite",
		"implemented the feature",
		"watch out for the timestamp drift"); err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}

	postCount := countCommits(t, repo)
	if postCount-preCount != 1 {
		t.Fatalf("expected exactly 1 new commit for CompleteTicket, got %d", postCount-preCount)
	}

	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(commit.Message, "complete ticket alpha/001") {
		t.Fatalf("unexpected commit message: %q", commit.Message)
	}
}

// countCommits walks the git log to count how many commits live on HEAD.
// Used by the auto-commit single-commit assertion.
func countCommits(t *testing.T, repo *git.Repository) int {
	t.Helper()
	head, err := repo.Head()
	if err != nil {
		// Fresh repo with no commits yet — count zero.
		return 0
	}
	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := iter.ForEach(func(_ *object.Commit) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count
}
