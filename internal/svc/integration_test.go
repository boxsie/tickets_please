// Package svc — T13 integration tests.
//
// These tests are deliberately cross-cutting: they exercise multiple service
// methods in concert to lock in the load-bearing invariants from SPEC §
// "Validation & enforcement", §"Atomicity", §"Embedding pipeline", §"Agent
// identity & sessions", and §"Project loading & in-memory cache". Per-method
// unit tests live alongside the source files they cover; this file is the
// safety net for behavior that only makes sense across method boundaries.
//
// All tests use the fake embed provider via newFakeEmbed(), so nothing here
// reaches out to Ollama or OpenAI.
package svc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/uuid"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// TestIntegration_FullLifecycle drives a ticket end-to-end — register agent
// (covered by setup), create project, create phase, create ticket in the
// phase, add a free-form comment, move the ticket (with a comment), complete
// it (with all three fields), then prove ListComments contains all three
// system+user comments in chronological order and SearchLearnings surfaces
// the completed ticket.
//
// One test, lots of moving parts on purpose: this is the "did the thing
// actually work end-to-end" canary.
func TestIntegration_FullLifecycle(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, agent := authedCtx(t, s)

	// 1. Project.
	proj, err := s.CreateProject(ctx, "lifecycle", "Lifecycle", "the canary",
		"This is the lifecycle test project. It exists to drive a ticket from creation through completion in a single test, exercising every method along the way. Two hundred chars-of-prose are required for a project summary, so this paragraph runs a little long on purpose.")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// 2. Phase under the project.
	phase, err := s.CreatePhase(ctx, "lifecycle", "Discovery", "the first phase",
		"This phase covers initial discovery work for the lifecycle project. It groups the early tickets so a planner can cluster them without writing dep edges between every pair, just like real phases do in the production project tree.")
	if err != nil {
		t.Fatalf("CreatePhase: %v", err)
	}

	// 3. Ticket inside the phase.
	phaseSlug := phase.Slug
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "lifecycle",
		Title:           "Wire the lifecycle",
		Body:            "Distinctive content for embedding-search round-trip: vermilion-platypus-token.",
		PhaseIDOrSlug:   &phaseSlug,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	// 4. Free-form user comment.
	userComment, err := s.CreateComment(ctx, tk.ID, "starting on this; tracking progress here")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	// 5. Move with required comment.
	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "picking this up now"); err != nil {
		t.Fatalf("MoveTicket: %v", err)
	}

	// 6. Complete with all three required fields.
	te := "ran the lifecycle integration test locally"
	ws := "wired CreateProject through CompleteTicket end-to-end"
	ln := "the canonical happy-path is what surfaces method-level regressions; vermilion-platypus-token"
	if _, err := s.CompleteTicket(ctx, tk.ID, te, ws, ln); err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}

	// 7. ListComments — user comment, system_move, system_completion in
	// chronological order. The user comment was created BEFORE MoveTicket so
	// it must come first.
	comments, err := s.ListComments(ctx, tk.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(comments) != 3 {
		t.Fatalf("expected 3 comments (user + system_move + system_completion), got %d: %+v", len(comments), comments)
	}
	if comments[0].ID != userComment.ID || comments[0].Kind != domain.CommentKindUser {
		t.Fatalf("comment[0] should be the user comment, got %+v", comments[0])
	}
	if comments[1].Kind != domain.CommentKindSystemMove {
		t.Fatalf("comment[1] should be system_move, got kind=%q", comments[1].Kind)
	}
	if comments[2].Kind != domain.CommentKindSystemCompletion {
		t.Fatalf("comment[2] should be system_completion, got kind=%q", comments[2].Kind)
	}

	// 8. SearchLearnings finds the completed ticket. Drain any in-flight
	// embed jobs (Flush is a barrier — blocks until the queue is empty).
	s.flushAllMountWorkers(ctx)
	waitForIdxLen(t, s.testLearningLen, 1, 5*time.Second)
	hits, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{Query: ln})
	if err != nil {
		t.Fatalf("SearchLearnings: %v", err)
	}
	if len(hits) == 0 || hits[0].TicketID != tk.ID {
		t.Fatalf("SearchLearnings did not surface completed ticket: got %+v", hits)
	}

	// Sanity: attribution survived round-trip.
	if comments[1].Author == nil || comments[1].Author.ID != agent.ID {
		t.Fatalf("system_move missing author %s: %+v", agent.ID, comments[1].Author)
	}
	if hits[0].ProjectID != proj.ID {
		t.Fatalf("learning hit's project id %s != %s", hits[0].ProjectID, proj.ID)
	}
}

// TestIntegration_MoveRequiresCommentOnDisk asserts the cross-method
// invariant: when MoveTicket succeeds, the system_move comment file is
// physically present on disk under the ticket's comments dir, not just in
// the in-memory cache. This is the load-bearing audit-trail rule from SPEC.
func TestIntegration_MoveRequiresCommentOnDisk(t *testing.T) {
	s, ctx, _, tk := moveCompleteScenario(t, config.Config{})
	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "going now"); err != nil {
		t.Fatalf("MoveTicket: %v", err)
	}

	commentsDir := filepath.Join(
		s.Store.Root, "tickets", "001-implement-feature", "comments",
	)
	entries, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatalf("read comments dir: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-system_move.md") {
			found = true
			data, err := os.ReadFile(filepath.Join(commentsDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "going now") {
				t.Fatalf("system_move file missing user note: %s", data)
			}
		}
	}
	if !found {
		t.Fatalf("no -system_move.md file in %v", entries)
	}
}

// TestIntegration_MoveToDoneRejectedPointsAtComplete locks in the
// self-documenting error message: when an LLM tries to move directly to done,
// the error mentions CompleteTicket so the model can self-correct.
func TestIntegration_MoveToDoneRejectedPointsAtComplete(t *testing.T) {
	s, ctx, _, tk := moveCompleteScenario(t, config.Config{})
	_, err := s.MoveTicket(ctx, tk.ID, domain.ColumnDone, "trying to skip ahead")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
	if !strings.Contains(err.Error(), "CompleteTicket") {
		t.Fatalf("error must mention CompleteTicket: %v", err)
	}
}

// TestIntegration_MoveFromDoneRejected — once a ticket is done it cannot be
// reopened (SPEC §Design decisions: "no reopen"). Returns FailedPrecondition.
func TestIntegration_MoveFromDoneRejected(t *testing.T) {
	s, ctx, _, tk := moveCompleteScenario(t, config.Config{})
	if _, err := s.CompleteTicket(ctx, tk.ID,
		"ran the suite", "shipped feature X", "watch out for race Y"); err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}
	_, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "reopen please")
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition, got %v", err)
	}
}

// TestIntegration_CompleteWritesAllArtifacts checks the full set of files +
// fields that CompleteTicket must produce: completion.md with three section
// headers, a system_completion comment file, ticket.yaml flipped to done,
// and completed_by populated.
func TestIntegration_CompleteWritesAllArtifacts(t *testing.T) {
	s, ctx, agent, tk := moveCompleteScenario(t, config.Config{})
	te := "ran the test suite locally"
	ws := "wired the new code path through"
	ln := "the audit trail must include three system+user comments after completion"
	got, err := s.CompleteTicket(ctx, tk.ID, te, ws, ln)
	if err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}

	dir := filepath.Join(s.Store.Root, "tickets", "001-implement-feature")

	// completion.md present and headed.
	completion, err := os.ReadFile(filepath.Join(dir, "completion.md"))
	if err != nil {
		t.Fatalf("read completion.md: %v", err)
	}
	for _, h := range []string{"## Testing evidence", "## Work summary", "## Learnings"} {
		if !strings.Contains(string(completion), h) {
			t.Fatalf("completion.md missing header %q", h)
		}
	}

	// system_completion comment file present.
	commentsDir := filepath.Join(dir, "comments")
	entries, err := os.ReadDir(commentsDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-system_completion.md") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing system_completion comment file: %v", entries)
	}

	// ticket.yaml has structured fields.
	yamlBytes, err := os.ReadFile(filepath.Join(dir, "ticket.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	yamlStr := string(yamlBytes)
	if !strings.Contains(yamlStr, "column: done") {
		t.Fatalf("ticket.yaml not flipped to done: %s", yamlStr)
	}
	if !strings.Contains(yamlStr, "completed_by: "+agent.ID) {
		t.Fatalf("ticket.yaml missing completed_by: %s", yamlStr)
	}
	if got.CompletedBy == nil || got.CompletedBy.ID != agent.ID {
		t.Fatalf("returned ticket missing completed_by: %+v", got.CompletedBy)
	}
}

// TestIntegration_CompleteIdempotency confirms the second CompleteTicket on
// the same ticket returns FailedPrecondition. We re-pass the same content;
// the rejection is structural (ticket is already done), not a content check.
func TestIntegration_CompleteIdempotency(t *testing.T) {
	s, ctx, _, tk := moveCompleteScenario(t, config.Config{})
	te := "ran the suite once"
	ws := "did the work once"
	ln := "the second call must FailedPrecondition; do not silently re-complete"
	if _, err := s.CompleteTicket(ctx, tk.ID, te, ws, ln); err != nil {
		t.Fatalf("first CompleteTicket: %v", err)
	}
	_, err := s.CompleteTicket(ctx, tk.ID, te, ws, ln)
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition on second complete, got %v", err)
	}
}

// TestIntegration_CommentsAreImmutable is the compile/runtime guard for SPEC
// §Design decisions: "Comments are immutable. No UpdateComment or
// DeleteComment RPCs."
//
// Reflection on the Service type confirms neither method exists. If a future
// refactor accidentally adds one, this test fires.
func TestIntegration_CommentsAreImmutable(t *testing.T) {
	typ := reflect.TypeOf((*Service)(nil))
	for _, banned := range []string{"UpdateComment", "DeleteComment"} {
		if _, ok := typ.MethodByName(banned); ok {
			t.Fatalf("Service must not expose %s — SPEC: comments are immutable", banned)
		}
	}
}

// TestIntegration_EmbeddingRoundTrip creates a ticket with distinctive content,
// waits for the worker, and confirms SearchTickets finds it. The fake provider
// is deterministic (sha256 → 768 floats) so an identical query string produces
// an identical query vector and cosine similarity ≈ 1.0.
func TestIntegration_EmbeddingRoundTrip(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	body := "kingfisher-velvet-paragon: a distinctive embedding-search string for the round-trip test"
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha",
		Title:           "round-trip-target",
		Body:            body,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Drain the worker so the embedding sidecar lands.
	s.flushAllMountWorkers(ctx)
	waitForIdxLen(t, s.testTicketLen, 1, 5*time.Second)

	// Search uses the same text that was indexed (title + "\n\n" + body).
	hits, err := s.SearchTickets(ctx, domain.SearchTicketsInput{
		Query:           "round-trip-target\n\n" + body,
		ProjectIDOrSlug: "alpha",
	})
	if err != nil {
		t.Fatalf("SearchTickets: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("SearchTickets returned no hits for the exact-text query")
	}
	if hits[0].Ticket.ID != tk.ID {
		t.Fatalf("expected top hit %s, got %s", tk.ID, hits[0].Ticket.ID)
	}
}

// TestIntegration_SearchLearningsFiltersToDone proves that SearchLearnings
// only returns completed tickets — even if an unfinished ticket has prose
// that semantically matches the query, it must not surface, because
// learnings are written only at completion time.
func TestIntegration_SearchLearningsFiltersToDone(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	// Ticket A — completed.
	tkA, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "ticket-a", Body: "alpha body",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Ticket B — left in todo. Deliberately uses a body that contains the
	// same distinctive token as A's learnings to prove that body alone does
	// NOT make a ticket appear in SearchLearnings.
	if _, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "ticket-b",
		Body: "kingfisher-aurora-token: this is in B's body but B is unfinished",
	}); err != nil {
		t.Fatal(err)
	}

	learningsText := "kingfisher-aurora-token: A's learnings include this exact text"
	if _, err := s.CompleteTicket(ctx, tkA.ID,
		"manually verified the change",
		"shipped ticket A end-to-end",
		learningsText,
	); err != nil {
		t.Fatal(err)
	}

	s.flushAllMountWorkers(ctx)
	waitForIdxLen(t, s.testLearningLen, 1, 5*time.Second)

	hits, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{Query: learningsText})
	if err != nil {
		t.Fatalf("SearchLearnings: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected exactly 1 learning hit (A only), got %d: %+v", len(hits), hits)
	}
	if hits[0].TicketID != tkA.ID {
		t.Fatalf("expected hit on A (%s), got %s", tkA.ID, hits[0].TicketID)
	}
}

// TestIntegration_BackfillRecoversMissingSidecar — bypass the worker by
// writing a ticket directly via the Store, then bring up a fresh Service
// against that data dir. The boot backfill walk should enqueue the missing
// body sidecar, which the worker writes within ~5s.
func TestIntegration_BackfillRecoversMissingSidecar(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}

	// Hand-write a ticket on disk, bypassing CreateTicket so no embedding
	// job is enqueued. (CreateProject already enqueued one for the project
	// summary; that sidecar will appear via the worker, which is fine.)
	tdir := filepath.Join(s.Store.Root, "tickets", "002-direct-write")
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &store.TicketRecord{
		ID:        uuid.NewString(),
		ProjectID: "alpha",
		Number:    2,
		Title:     "direct-write",
		Column:    domain.ColumnTodo,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.WriteYAMLAtomic(filepath.Join(tdir, "ticket.yaml"), rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(tdir, "body.md"),
		[]byte("backfill-target: this body has no sidecar yet\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	bodySidecar := filepath.Join(tdir, "body.embedding.json")
	if _, err := os.Stat(bodySidecar); err == nil {
		t.Fatal("body sidecar already present before backfill — test setup is wrong")
	}

	// Tear down the running service and bring up a fresh one against the
	// same data dir; the new service's backfill walk picks up the gap.
	s.Close()
	s2 := freshServiceWithCfg(t, config.Config{DataDir: s.Store.Root})
	if !waitForFile(bodySidecar, 5*time.Second) {
		t.Fatalf("backfill did not regenerate body sidecar at %s", bodySidecar)
	}
	_ = ctx
	_ = s2 // teardown via t.Cleanup
}

// TestIntegration_ProjectCacheEvictionReloads — drive the cache eviction
// path explicitly. We can't realistically use ProjectIdleMinutes=0 because
// New(cfg) treats that as "use default 15m"; instead, we drive eviction
// via the Cache.Evict helper to prove the next access reloads cleanly.
//
// The reload itself is the meaningful invariant; how eviction is triggered
// (idle TTL, LRU cap, fsnotify, manual evict) is incidental.
func TestIntegration_ProjectCacheEvictionReloads(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	// Warm the cache.
	if _, err := s.GetProject(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if s.Cache.Len() != 1 {
		t.Fatalf("expected cache len 1 after GetProject, got %d", s.Cache.Len())
	}

	// Evict.
	s.Cache.Evict("alpha")
	if s.Cache.Len() != 0 {
		t.Fatalf("expected cache empty after Evict, got %d", s.Cache.Len())
	}

	// Next access lazy-reloads.
	p, err := s.GetProject(ctx, "alpha")
	if err != nil {
		t.Fatalf("GetProject after evict: %v", err)
	}
	if p.Slug != "alpha" {
		t.Fatalf("got slug %q after reload", p.Slug)
	}
	if s.Cache.Len() != 1 {
		t.Fatalf("expected cache len 1 after reload, got %d", s.Cache.Len())
	}
}

// TestIntegration_AgentSessionExpiry — backdate an agent's expires_at via the
// store helper (mirrors the existing agents_test.go style; setting
// AgentSessionTTLMinutes=0 falls back to the 60m default in svc.New). A
// mutating call with the expired session must return ErrUnauthenticated.
//
// Uses a non-bootstrap mutation (CreateTicket on an existing project) because
// CreateProject is intentionally auth-soft and would silently no-op the
// expired session — see TestCreateProject_NoSessionSucceedsWithNilCreatedBy.
func TestIntegration_AgentSessionExpiry(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx := context.Background()

	// Bootstrap: create a project under a fresh authed session so we have
	// somewhere to attempt a follow-on mutation.
	bootCtx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(bootCtx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatalf("bootstrap CreateProject: %v", err)
	}

	id, _, err := s.RegisterAgent(ctx, "expiring-key", "ExpiringAgent", nil, 0, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	rec, err := s.AgentStore.ReadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	rec.ExpiresAt = time.Now().Add(-time.Minute)
	if err := s.AgentStore.WriteAgentRecord(rec); err != nil {
		t.Fatal(err)
	}

	authed := WithSessionID(ctx, id)
	_, err = s.CreateTicket(authed, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha",
		Title:           "expired write",
		Body:            "should reject",
	})
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for expired session, got %v", err)
	}
}

// TestIntegration_ActiveKeyUniqueness — two RegisterAgent calls with the same
// key while the first session is active must fail with ErrAlreadyExists. Once
// the first expires, the key may be reused (covered by the existing
// agents_test.go; here we only assert the active-key path).
func TestIntegration_ActiveKeyUniqueness(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx := context.Background()
	if _, _, err := s.RegisterAgent(ctx, "shared-key", "First", nil, 0, ""); err != nil {
		t.Fatalf("first RegisterAgent: %v", err)
	}
	_, _, err := s.RegisterAgent(ctx, "shared-key", "Second", nil, 0, "")
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists for duplicate active key, got %v", err)
	}
}

// TestIntegration_AutoCommitInRealGitRepo — init a real git repo, point the
// data dir inside it, drive create → move → complete, and confirm three
// commits land in `git log` authored as the calling agent. The single-commit
// asserts on each individual op are covered elsewhere; this test is the
// cross-method "all three mutations contribute to the audit trail" check.
func TestIntegration_AutoCommitInRealGitRepo(t *testing.T) {
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

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create the project (this is its own commit, but we only count
	// the create/move/complete commits below, so capture the post-create
	// baseline.)
	if _, err := s.CreateProject(ctx, "auto", "Auto", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	baseline := countCommits(t, repo)

	// 1. Create ticket.
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "auto", Title: "Audit me", Body: "trail this",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 2. Move.
	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "starting"); err != nil {
		t.Fatal(err)
	}
	// 3. Complete.
	if _, err := s.CompleteTicket(ctx, tk.ID,
		"ran the suite", "shipped the audit-trail check", "git log lines align with mutations"); err != nil {
		t.Fatal(err)
	}

	post := countCommits(t, repo)
	if post-baseline != 3 {
		t.Fatalf("expected 3 commits (create+move+complete), got %d", post-baseline)
	}

	// Walk the three most-recent commits and confirm each was authored as
	// the agent. Walking via Log ensures we examine every parent in turn.
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	if err := iter.ForEach(func(c *object.Commit) error {
		if seen >= 3 {
			return nil
		}
		seen++
		if c.Author.Name != agent.Name {
			t.Errorf("commit %s author=%q, want %q", c.Hash, c.Author.Name, agent.Name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
