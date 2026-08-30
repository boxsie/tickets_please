package svc

import (
	"sync"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// seedOldArchiveableTicket builds a project + a single ticket and then ages
// it via direct mutation of the cached domain.Ticket so the Decide policy
// considers it old enough to archive. Returns the ticket id.
func seedOldArchiveableTicket(t *testing.T, s *Service, slug, title string) string {
	t.Helper()
	ctx, _ := authedCtx(t, s)
	if _, err := s.CreateProject(ctx, slug, slug, "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: title, Body: "old body",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Age the ticket: complete it with a 200-day-old CompletedAt.
	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "start"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveTicket(ctx, tk.ID, domain.ColumnTesting, "testing"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTicket(ctx, tk.ID,
		"tested extensively", "did the thing", "useful learning here"); err != nil {
		t.Fatal(err)
	}
	// Mutate the cache directly so the ticket is "old". Real-world tests of
	// the archive sweep would use a fake clock; this hand-mutation is
	// acceptable for hobby-scale because it touches only the cached value,
	// not on-disk state — and ApplyArchivePolicy reads from the cache.
	lp, _, err := s.Cache.Get(ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	lp.Lock.Lock()
	old := time.Now().AddDate(0, 0, -200)
	t1 := lp.Tickets[tk.ID]
	t1.CompletedAt = &old
	lp.Lock.Unlock()
	return tk.ID
}

// seedMinimumRetrievals bumps the ticket's retrievals counter to match
// policy.MinRetrievals so the aged branch fires.
func seedMinimumRetrievals(t *testing.T, mount *ProjectMount, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_ = mount.Feedback.RecordRetrieval(nil, []domain.EntryKey{domain.TicketEntryKey(id)})
	}
}

// enablePolicy sets the mount's ArchivePolicy to enabled with sensible
// defaults so the sweep can run.
func enablePolicy(mount *ProjectMount) {
	p := defaultArchivePolicy()
	p.Enabled = true
	mount.ArchivePolicy = p
}

func TestApplyArchivePolicy_DryRunReportsButDoesntMutate(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	id := seedOldArchiveableTicket(t, s, "alpha", "old ticket")
	mount := s.mountForSlug("alpha")
	if mount == nil {
		t.Fatal("mount missing")
	}
	enablePolicy(mount)
	seedMinimumRetrievals(t, mount, id, 5)

	report, err := s.ApplyArchivePolicy(ctx, ApplyPolicyInput{ProjectIDOrSlug: "alpha"})
	if err != nil {
		t.Fatalf("ApplyArchivePolicy dry-run: %v", err)
	}
	if len(report.WouldArchive) != 1 || report.WouldArchive[0].TicketID != id {
		t.Errorf("would_archive = %v, want 1 entry for %s", report.WouldArchive, id)
	}
	if len(report.Archived) != 0 {
		t.Errorf("dry-run produced Archived entries: %v", report.Archived)
	}
	// Cache state: ticket still not archived.
	tk, err := s.GetTicket(ctx, id)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if tk.Archived {
		t.Errorf("dry-run mutated state — ticket is archived")
	}
	// Idempotent: a second dry-run reports the same.
	report2, _ := s.ApplyArchivePolicy(ctx, ApplyPolicyInput{ProjectIDOrSlug: "alpha"})
	if len(report2.WouldArchive) != 1 {
		t.Errorf("second dry-run unstable: %v", report2.WouldArchive)
	}
}

func TestApplyArchivePolicy_CommitFlipsFlags(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	id := seedOldArchiveableTicket(t, s, "alpha", "old ticket")
	mount := s.mountForSlug("alpha")
	enablePolicy(mount)
	seedMinimumRetrievals(t, mount, id, 5)

	report, err := s.ApplyArchivePolicy(ctx, ApplyPolicyInput{ProjectIDOrSlug: "alpha", Commit: true})
	if err != nil {
		t.Fatalf("ApplyArchivePolicy commit: %v", err)
	}
	if len(report.Archived) != 1 || report.Archived[0].TicketID != id {
		t.Errorf("archived = %v, want 1 entry for %s", report.Archived, id)
	}

	tk, err := s.GetTicket(ctx, id)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if !tk.Archived || tk.ArchivedAt == nil {
		t.Errorf("commit didn't archive: %+v", tk)
	}
	// Default search excludes.
	listed, _, err := s.ListTickets(ctx, domain.ListTicketsInput{ProjectIDOrSlug: "alpha"})
	if err != nil {
		t.Fatalf("ListTickets: %v", err)
	}
	for _, x := range listed {
		if x.ID == id {
			t.Errorf("post-commit default list still surfaced archived ticket")
		}
	}
}

func TestApplyArchivePolicy_RefusesWhenDisabled(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	_ = seedOldArchiveableTicket(t, s, "alpha", "old ticket")
	// Don't enable the policy — default mount.ArchivePolicy.Enabled = false.
	_, err := s.ApplyArchivePolicy(ctx, ApplyPolicyInput{ProjectIDOrSlug: "alpha"})
	if err == nil {
		t.Fatal("expected refusal when policy disabled")
	}
}

func TestApplyArchivePolicy_LimitRespected(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	// Three archiveable tickets.
	var ids []string
	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
			ProjectIDOrSlug: "alpha", Title: "old-" + itoa(i), Body: "body",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = s.MoveTicket(ctx, tk.ID, domain.ColumnInProgress, "start")
		_, _ = s.MoveTicket(ctx, tk.ID, domain.ColumnTesting, "testing")
		_, _ = s.CompleteTicket(ctx, tk.ID, "tested it", "did stuff", "the learning")
		ids = append(ids, tk.ID)
	}
	lp, _, _ := s.Cache.Get(ctx, "alpha")
	lp.Lock.Lock()
	old := time.Now().AddDate(0, 0, -200)
	for _, id := range ids {
		lp.Tickets[id].CompletedAt = &old
	}
	lp.Lock.Unlock()
	mount := s.mountForSlug("alpha")
	enablePolicy(mount)
	for _, id := range ids {
		seedMinimumRetrievals(t, mount, id, 5)
	}

	report, err := s.ApplyArchivePolicy(ctx, ApplyPolicyInput{
		ProjectIDOrSlug: "alpha", Commit: true, Limit: 2,
	})
	if err != nil {
		t.Fatalf("ApplyArchivePolicy: %v", err)
	}
	if len(report.Archived) != 2 {
		t.Errorf("Archived len = %d, want 2 (limit honored)", len(report.Archived))
	}
}

// TestApplyArchivePolicy_ConcurrentCallsSerialize: two callers hitting the
// service simultaneously each get a coherent report; no double-archive of any
// single ticket. Cross-checks the lock chain — service → ArchiveTicket → flock.
func TestApplyArchivePolicy_ConcurrentCallsSerialize(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)
	id := seedOldArchiveableTicket(t, s, "alpha", "old ticket")
	mount := s.mountForSlug("alpha")
	enablePolicy(mount)
	seedMinimumRetrievals(t, mount, id, 5)

	var wg sync.WaitGroup
	wg.Add(2)
	var archivedCount, errCount int
	var mu sync.Mutex
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			report, err := s.ApplyArchivePolicy(ctx, ApplyPolicyInput{ProjectIDOrSlug: "alpha", Commit: true})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errCount++
				return
			}
			archivedCount += len(report.Archived)
		}()
	}
	wg.Wait()
	// Exactly one caller should have archived the ticket; the other gets it
	// already-archived and reports either 0 archived (its candidate snapshot
	// was empty by the time it scanned) OR records a skipped row. Either
	// way, the total archived ticket count is 1.
	tk, _ := s.GetTicket(ctx, id)
	if !tk.Archived {
		t.Errorf("ticket should be archived")
	}
	if archivedCount > 1 {
		t.Errorf("ticket archived more than once: archivedCount=%d", archivedCount)
	}
}
