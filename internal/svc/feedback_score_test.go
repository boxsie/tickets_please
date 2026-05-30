package svc

import (
	"math"
	"testing"
	"time"

	"tickets_please/internal/config"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

func TestQualityParams_Multiplier_Defaults(t *testing.T) {
	p := defaultQualityParams()
	cases := []struct {
		likes, dislikes int
		want            float64
	}{
		{0, 0, 0.75},                          // unrated: quality=0.5 → 0.5 + 0.5*0.5
		{5, 0, 0.5 + 0.5*(7.0/9.0)},           // mild like skew
		{0, 5, 0.5 + 0.5*(2.0/9.0)},           // mild dislike skew
		{1000, 0, 0.5 + 0.5*(1002.0/1004.0)},  // overwhelmingly liked
	}
	for _, c := range cases {
		got := p.Multiplier(c.likes, c.dislikes)
		if math.Abs(got-c.want) > 1e-6 {
			t.Errorf("Multiplier(%d, %d) = %v, want %v", c.likes, c.dislikes, got, c.want)
		}
		if got < p.MinMultiplier || got > 1.0+1e-9 {
			t.Errorf("Multiplier(%d, %d) = %v out of range [%v, 1.0]", c.likes, c.dislikes, got, p.MinMultiplier)
		}
	}
}

func TestQualityParams_Multiplier_KillSwitch(t *testing.T) {
	p := defaultQualityParams()
	p.Enabled = false
	for _, n := range []int{0, 5, 100} {
		if got := p.Multiplier(n, n); got != 1.0 {
			t.Errorf("disabled multiplier = %v, want 1.0 (got input likes=dislikes=%d)", got, n)
		}
	}
}

func TestQualityParams_Multiplier_MonotoneInLikes(t *testing.T) {
	p := defaultQualityParams()
	prev := p.Multiplier(0, 0)
	for likes := 1; likes <= 20; likes++ {
		now := p.Multiplier(likes, 0)
		if now < prev {
			t.Fatalf("multiplier decreased from %v to %v as likes went %d→%d", prev, now, likes-1, likes)
		}
		prev = now
	}
}

func TestQualityParams_Multiplier_MonotoneInDislikes(t *testing.T) {
	p := defaultQualityParams()
	prev := p.Multiplier(0, 0)
	for d := 1; d <= 20; d++ {
		now := p.Multiplier(0, d)
		if now > prev {
			t.Fatalf("multiplier increased from %v to %v as dislikes went %d→%d", prev, now, d-1, d)
		}
		prev = now
	}
}

// TestSearchTickets_FeedbackReordersByQuality: seed two tickets with
// IDENTICAL bodies (so cosine is equal), like one heavily; rescore should
// promote the liked ticket to the top.
func TestSearchTickets_FeedbackReordersByQuality(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk1, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "feedback-rank-test", Body: "same body for both tickets",
	})
	if err != nil {
		t.Fatal(err)
	}
	tk2, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "feedback-rank-test", Body: "same body for both tickets",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForIdxLen(t, s.testTicketLen, 2, 5*time.Second)

	mount := s.mountForSlug("alpha")
	if mount == nil || mount.Feedback == nil {
		t.Fatal("mount or feedback missing")
	}
	for i := 0; i < 20; i++ {
		if err := mount.Feedback.RecordRating(ctx, domain.TicketEntryKey(tk2.ID), domain.RatingLike, ""); err != nil {
			t.Fatalf("RecordRating: %v", err)
		}
	}

	// The indexed text is `Title + "\n\n" + Body` (see hydrateTicketBody);
	// match it exactly so fakeEmbed gives cosine ≈ 1.0 — positive scores let
	// the multiplier (∈[0.5, 1.0]) order things as the spec intends.
	hits, err := s.SearchTickets(ctx, domain.SearchTicketsInput{
		Query:           "feedback-rank-test\n\nsame body for both tickets",
		ProjectIDOrSlug: "alpha",
		Limit:           2,
	})
	if err != nil {
		t.Fatalf("SearchTickets: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if hits[0].Ticket.ID != tk2.ID {
		t.Errorf("top hit = %s, want liked tk2 = %s", hits[0].Ticket.ID, tk2.ID)
	}
	if hits[1].Ticket.ID != tk1.ID {
		t.Errorf("second hit = %s, want unrated tk1 = %s", hits[1].Ticket.ID, tk1.ID)
	}
	if hits[0].Score <= 0 || hits[0].RawScore <= 0 {
		t.Errorf("hits[0] scores not populated: score=%v raw=%v", hits[0].Score, hits[0].RawScore)
	}
	if hits[0].Score >= hits[0].RawScore+1e-6 {
		t.Errorf("liked hit's adjusted score %v should be <= raw %v (multiplier <= 1.0)", hits[0].Score, hits[0].RawScore)
	}
	wantMul := 0.75
	gotMul := float64(hits[1].Score) / float64(hits[1].RawScore)
	if math.Abs(gotMul-wantMul) > 0.01 {
		t.Errorf("tk1 multiplier (adjusted/raw) = %v, want ~%v", gotMul, wantMul)
	}
}

// TestSearchTickets_KillSwitchDisablesMultiplier: with mount.QualityParams.
// Enabled = false, rescore is skipped and Score == RawScore.
func TestSearchTickets_KillSwitchDisablesMultiplier(t *testing.T) {
	s := freshServiceWithCfg(t, config.Config{})
	ctx, _ := authedCtx(t, s)

	if _, err := s.CreateProject(ctx, "alpha", "Alpha", "", validSummary()); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket(ctx, domain.CreateTicketInput{
		ProjectIDOrSlug: "alpha", Title: "killswitch-test", Body: "body for killswitch test",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForIdxLen(t, s.testTicketLen, 1, 5*time.Second)

	mount := s.mountForSlug("alpha")
	if mount == nil {
		t.Fatal("mount missing")
	}
	mount.QualityParams = QualityParams{Enabled: false}

	hits, err := s.SearchTickets(ctx, domain.SearchTicketsInput{
		Query:           "killswitch-test\n\nbody for killswitch test",
		ProjectIDOrSlug: "alpha",
	})
	if err != nil {
		t.Fatalf("SearchTickets: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if hits[0].Ticket.ID != tk.ID {
		t.Errorf("hit[0] = %s, want %s", hits[0].Ticket.ID, tk.ID)
	}
	if hits[0].Score != hits[0].RawScore {
		t.Errorf("kill switch on: Score (%v) should equal RawScore (%v)", hits[0].Score, hits[0].RawScore)
	}
}

// TestExpandedRawLimit covers the over-fetch ceiling: 2× expansion, capped
// at searchMaxLimit (50).
func TestExpandedRawLimit(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{1, 2}, {5, 10}, {10, 20}, {25, 50}, {26, 50}, {50, 50}, {100, 50},
	}
	for _, c := range cases {
		if got := expandedRawLimit(c.in); got != c.want {
			t.Errorf("expandedRawLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestResolveQualityParams covers the per-project override merge: nil block
// → all defaults; partial override → only specified fields differ.
func TestResolveQualityParams(t *testing.T) {
	defaults := defaultQualityParams()
	if got := resolveQualityParams(nil); got != defaults {
		t.Errorf("nil record = %+v, want defaults %+v", got, defaults)
	}
	enabledFalse := false
	rec := &store.FeedbackConfigRecord{Enabled: &enabledFalse}
	got := resolveQualityParams(rec)
	if got.Enabled {
		t.Errorf("Enabled=false override didn't land: %+v", got)
	}
	if got.Alpha != defaults.Alpha {
		t.Errorf("non-overridden Alpha changed: %v", got.Alpha)
	}
}
