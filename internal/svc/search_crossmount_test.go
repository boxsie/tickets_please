package svc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/vecindex"
)

// seedRepoWithCompletedTicket builds a minimal on-disk repo at <parent>/<dirName>:
//
//	<repo>/.tickets_please/project.yaml
//	<repo>/.tickets_please/summary.md      (+ summary.embedding.json)
//	<repo>/.tickets_please/tickets/001-<ticketSlug>/
//	  ticket.yaml + body.md + completion.md (+ learnings.embedding.json)
//
// The summary + learnings sidecars are pre-computed via fakeEmbed so the
// resident indexes can hydrate without any worker work.
func seedRepoWithCompletedTicket(
	t *testing.T,
	parent, dirName, slug, ticketSlug string,
	summaryText, learningsText string,
) (repo string, projectID, ticketID string) {
	t.Helper()
	repo = filepath.Join(parent, dirName)
	dataDir := filepath.Join(repo, ".tickets_please")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir dataDir: %v", err)
	}

	now := time.Now()
	projectID = uuid.NewString()
	if err := store.WriteYAMLAtomic(filepath.Join(dataDir, "project.yaml"), &store.ProjectRecord{
		ID:        projectID,
		Slug:      slug,
		Name:      slug,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("write project.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "summary.md"), []byte(summaryText), 0o644); err != nil {
		t.Fatalf("write summary.md: %v", err)
	}

	// Pre-compute the project summary embedding so hydrate Upserts directly.
	fe := newFakeEmbed()
	vec, err := fe.Embed(context.Background(), summaryText)
	if err != nil {
		t.Fatalf("fakeEmbed summary: %v", err)
	}
	if err := vecindex.WriteSidecar(filepath.Join(dataDir, "summary.embedding.json"), vecindex.Sidecar{
		// Match what the in-process worker stamps under freshServiceNoDataDir's
		// fake provider: Name()="fake", and mount.EmbedModel resolves to ""
		// because the test cfg leaves OllamaModel blank. Hand-seeded sidecars
		// have to match or W2-T3 staleness detection evicts them.
		Provider: "fake",
		Model:    "",
		Dim:      len(vec),
		Vec:      vec,
	}); err != nil {
		t.Fatalf("write summary sidecar: %v", err)
	}

	// Completed ticket.
	ticketID = uuid.NewString()
	tdir := filepath.Join(dataDir, "tickets", "001-"+ticketSlug)
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}
	completedAt := now
	if err := store.WriteYAMLAtomic(filepath.Join(tdir, "ticket.yaml"), &store.TicketRecord{
		ID:          ticketID,
		ProjectID:   projectID,
		Number:      1,
		Title:       ticketSlug,
		Column:      domain.ColumnDone,
		CompletedAt: &completedAt,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("write ticket.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tdir, "body.md"), []byte(ticketSlug+" body"), 0o644); err != nil {
		t.Fatalf("write body.md: %v", err)
	}
	completion := "## Testing evidence\nverified manually\n\n## Work summary\nshipped it\n\n## Learnings\n" + learningsText + "\n"
	if err := os.WriteFile(filepath.Join(tdir, "completion.md"), []byte(completion), 0o644); err != nil {
		t.Fatalf("write completion.md: %v", err)
	}
	// Pre-computed learnings embedding so SearchLearnings hits cleanly.
	lvec, err := fe.Embed(context.Background(), learningsText)
	if err != nil {
		t.Fatalf("fakeEmbed learnings: %v", err)
	}
	if err := vecindex.WriteSidecar(filepath.Join(tdir, "learnings.embedding.json"), vecindex.Sidecar{
		// Provider/Model match the mount's stamp; see the summary block above.
		Provider: "fake",
		Model:    "",
		Dim:      len(lvec),
		Vec:      lvec,
	}); err != nil {
		t.Fatalf("write learnings sidecar: %v", err)
	}

	return repo, projectID, ticketID
}

// TestSearchLearnings_AggregatesAcrossMounts mounts two distinct repos with
// completed tickets and verifies SearchLearnings returns hits from both with
// the right slug provenance, then evicts one and confirms its hits disappear,
// then re-mounts and confirms they return.
func TestSearchLearnings_AggregatesAcrossMounts(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 4})
	tmp := t.TempDir()

	alphaLearnings := "the alpha lesson is that idempotency tokens prevent retry storms"
	betaLearnings := "the beta lesson is that watchdog timers must reset before I/O"

	repoA, _, ticketA := seedRepoWithCompletedTicket(t,
		tmp, "repoAlpha", "alpha", "alpha-ticket",
		distinctSummary("alpha-summary"), alphaLearnings,
	)
	repoB, _, ticketB := seedRepoWithCompletedTicket(t,
		tmp, "repoBeta", "beta", "beta-ticket",
		distinctSummary("beta-summary"), betaLearnings,
	)

	if _, err := s.RegisterProjectMount(context.Background(), repoA); err != nil {
		t.Fatalf("mount alpha: %v", err)
	}
	if _, err := s.RegisterProjectMount(context.Background(), repoB); err != nil {
		t.Fatalf("mount beta: %v", err)
	}

	// Hydration is synchronous for entries with on-disk sidecars, so both
	// learnings should be in the index immediately.
	if got := s.testLearningLen(); got != 2 {
		t.Fatalf("LearningsIdx after mount = %d; want 2", got)
	}

	ctx, _ := authedCtx(t, s)

	// Cross-mount search: query alpha's learnings text → top hit is alpha,
	// beta still appears among the hits.
	hits, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{Query: alphaLearnings})
	if err != nil {
		t.Fatalf("SearchLearnings(alpha text): %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("want >=2 hits across mounts; got %d", len(hits))
	}
	if hits[0].TicketID != ticketA {
		t.Errorf("top hit ticket = %s; want %s (alpha)", hits[0].TicketID, ticketA)
	}
	if hits[0].ProjectSlug != "alpha" {
		t.Errorf("top hit slug = %q; want alpha", hits[0].ProjectSlug)
	}
	gotSlugs := map[string]bool{}
	for _, h := range hits {
		gotSlugs[h.ProjectSlug] = true
	}
	if !gotSlugs["alpha"] || !gotSlugs["beta"] {
		t.Errorf("missing slug in cross-mount hits: %v", gotSlugs)
	}

	// Project-scoped search: only alpha.
	hitsAlpha, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{
		Query:           alphaLearnings,
		ProjectIDOrSlug: "alpha",
	})
	if err != nil {
		t.Fatalf("SearchLearnings(alpha scope): %v", err)
	}
	for _, h := range hitsAlpha {
		if h.TicketID == ticketB {
			t.Errorf("beta ticket leaked through alpha scope")
		}
		if h.ProjectSlug != "alpha" {
			t.Errorf("scoped hit slug = %q; want alpha", h.ProjectSlug)
		}
	}

	// Force-evict beta by manually nilling its Store + dropping its index
	// entries (mirrors what maybeEvictLocked does internally).
	s.mountsMu.Lock()
	s.projectMounts["beta"].Store = nil
	s.dropMountFromIndexes("beta")
	s.mountsMu.Unlock()

	hitsAfterEvict, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{Query: betaLearnings})
	if err != nil {
		t.Fatalf("SearchLearnings post-evict: %v", err)
	}
	for _, h := range hitsAfterEvict {
		if h.ProjectSlug == "beta" {
			t.Errorf("beta hit returned after eviction: %+v", h)
		}
	}

	// Re-resolve beta → re-hydrates → its hit returns.
	if _, err := s.ResolveProjectStore(context.Background(), "beta"); err != nil {
		t.Fatalf("resolve beta after evict: %v", err)
	}
	if got := s.testLearningLen(); got != 2 {
		t.Fatalf("LearningsIdx after re-mount = %d; want 2", got)
	}
	hitsRehyd, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{Query: betaLearnings})
	if err != nil {
		t.Fatalf("SearchLearnings post-remount: %v", err)
	}
	if len(hitsRehyd) == 0 || hitsRehyd[0].TicketID != ticketB {
		t.Errorf("post-remount top hit = %+v; want %s (beta)", hitsRehyd, ticketB)
	}
}

// TestSearchLearnings_DropOnEvictThenRemount races a register loop against a
// concurrent SearchLearnings caller — under -race this catches data races on
// the resident-index + mounts mutations the new hydrate / drop paths added.
func TestSearchLearnings_ConcurrentMountAndSearch(t *testing.T) {
	s := freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 32})
	tmp := t.TempDir()

	// Pre-create five repos.
	const n = 5
	repos := make([]string, n)
	for i := 0; i < n; i++ {
		slug := fmt.Sprintf("p-%d", i)
		repos[i], _, _ = seedRepoWithCompletedTicket(t,
			tmp, "repo-"+slug, slug, slug+"-ticket",
			distinctSummary(slug+"-summary"),
			"learnings for "+slug+" with enough length to be useful",
		)
	}

	ctx, _ := authedCtx(t, s)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Searcher goroutine — runs SearchLearnings on a tight loop until stop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, err := s.SearchLearnings(ctx, domain.SearchLearningsInput{Query: "learnings"})
			if err != nil && !strings.Contains(err.Error(), "not mounted") {
				t.Errorf("SearchLearnings race iteration: %v", err)
				return
			}
		}
	}()

	// Five mount goroutines.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if _, err := s.RegisterProjectMount(context.Background(), repos[idx]); err != nil {
				t.Errorf("register %d: %v", idx, err)
			}
		}(i)
	}

	// Let the mounts settle and the searcher do a few hundred iterations.
	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()

	if got := s.testLearningLen(); got != n {
		t.Fatalf("LearningsIdx after race = %d; want %d", got, n)
	}
}
