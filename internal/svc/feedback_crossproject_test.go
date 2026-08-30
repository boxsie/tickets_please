package svc

import (
	"context"
	"testing"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
)

// mountAlphaBeta stands up two seeded repos with one completed ticket each and
// returns the service plus both ticket ids. Mirrors the fixture the
// cross-mount search tests use.
func mountAlphaBeta(t *testing.T) (s *Service, ticketA, ticketB string) {
	t.Helper()
	s = freshServiceNoDataDir(t, config.Config{MaxLoadedProjects: 4})
	tmp := t.TempDir()

	repoA, _, ticketA := seedRepoWithCompletedTicket(t,
		tmp, "repoAlpha", "alpha", "alpha-ticket",
		distinctSummary("alpha-summary"), "the alpha lesson is that idempotency tokens prevent retry storms",
	)
	repoB, _, ticketB := seedRepoWithCompletedTicket(t,
		tmp, "repoBeta", "beta", "beta-ticket",
		distinctSummary("beta-summary"), "the beta lesson is that watchdog timers must reset before I/O",
	)
	if _, err := s.RegisterProjectMount(context.Background(), repoA); err != nil {
		t.Fatalf("mount alpha: %v", err)
	}
	if _, err := s.RegisterProjectMount(context.Background(), repoB); err != nil {
		t.Fatalf("mount beta: %v", err)
	}
	return s, ticketA, ticketB
}

// TestRateSearchResult_CrossProjectEntryLandsInOwningProject is the ticket-145
// regression: a cross_project search hands back keys from other projects, and
// rating one of them used to be rejected outright — so the hits most in need of
// a correction were the only ones that couldn't receive one. The rating must
// land, and it must land in the project that owns the entry.
func TestRateSearchResult_CrossProjectEntryLandsInOwningProject(t *testing.T) {
	s, _, ticketB := mountAlphaBeta(t)
	ctx, _ := authedCtx(t, s)

	key := domain.LearningEntryKey(ticketB)
	out, err := s.RateSearchResult(ctx, RateInput{
		ProjectIDOrSlug: "alpha", // bound to alpha, rating one of beta's entries
		EntryKeys:       []domain.EntryKey{key},
		Rating:          domain.RatingDislike,
		Reason:          "off-topic for the query that surfaced it",
	})
	if err != nil {
		t.Fatalf("RateSearchResult: %v", err)
	}
	if len(out.Rejected) != 0 {
		t.Fatalf("expected no rejections, got %+v", out.Rejected)
	}
	if len(out.Updated) != 1 {
		t.Fatalf("expected 1 updated entry, got %d", len(out.Updated))
	}
	if got := out.Updated[0].ProjectSlug; got != "beta" {
		t.Errorf("rating attributed to project %q; want beta (the owner)", got)
	}
	if got := out.Updated[0].Dislikes; got != 1 {
		t.Errorf("dislikes = %d; want 1", got)
	}

	// The write must be in beta's store, not the session's project.
	betaRec, ok := s.mountForSlug("beta").Feedback.Get(key)
	if !ok || betaRec.Dislikes != 1 {
		t.Errorf("beta feedback store: rec=%+v ok=%v; want 1 dislike", betaRec, ok)
	}
	if _, ok := s.mountForSlug("alpha").Feedback.Get(key); ok {
		t.Error("rating leaked into alpha's feedback store; it belongs to beta")
	}
}

// TestRateSearchResult_MixedBatchFansOut: one call rating hits from both
// projects splits across both feedback stores, which is the exact shape a
// cross_project search result set arrives in.
func TestRateSearchResult_MixedBatchFansOut(t *testing.T) {
	s, ticketA, ticketB := mountAlphaBeta(t)
	ctx, _ := authedCtx(t, s)

	keyA := domain.LearningEntryKey(ticketA)
	keyB := domain.LearningEntryKey(ticketB)
	out, err := s.RateSearchResult(ctx, RateInput{
		ProjectIDOrSlug: "alpha",
		EntryKeys:       []domain.EntryKey{keyA, keyB},
		Rating:          domain.RatingLike,
	})
	if err != nil {
		t.Fatalf("RateSearchResult: %v", err)
	}
	if len(out.Rejected) != 0 {
		t.Fatalf("expected no rejections, got %+v", out.Rejected)
	}
	bySlug := map[string]domain.EntryKey{}
	for _, u := range out.Updated {
		bySlug[u.ProjectSlug] = u.EntryKey
	}
	if bySlug["alpha"] != keyA {
		t.Errorf("alpha entry = %q; want %q", bySlug["alpha"], keyA)
	}
	if bySlug["beta"] != keyB {
		t.Errorf("beta entry = %q; want %q", bySlug["beta"], keyB)
	}
	for slug, key := range map[string]domain.EntryKey{"alpha": keyA, "beta": keyB} {
		rec, ok := s.mountForSlug(slug).Feedback.Get(key)
		if !ok || rec.Likes != 1 {
			t.Errorf("%s feedback store: rec=%+v ok=%v; want 1 like", slug, rec, ok)
		}
	}
}

// TestRateSearchResult_UnownedKeyStillRejected: widening the search for an
// owner must not turn "this entry doesn't exist" into a silent success.
func TestRateSearchResult_UnownedKeyStillRejected(t *testing.T) {
	s, _, _ := mountAlphaBeta(t)
	ctx, _ := authedCtx(t, s)

	out, err := s.RateSearchResult(ctx, RateInput{
		ProjectIDOrSlug: "alpha",
		EntryKeys: []domain.EntryKey{
			domain.LearningEntryKey("00000000-0000-0000-0000-000000000000"),
			domain.EntryKey("not-a-valid-key"),
		},
		Rating: domain.RatingLike,
	})
	if err != nil {
		t.Fatalf("RateSearchResult: %v", err)
	}
	if len(out.Updated) != 0 {
		t.Fatalf("expected nothing updated, got %+v", out.Updated)
	}
	if len(out.Rejected) != 2 {
		t.Fatalf("expected both keys rejected, got %+v", out.Rejected)
	}
}
