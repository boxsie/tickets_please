package projects

import (
	"context"
	"strings"
	"testing"
)

func renderRating(t *testing.T, p RatingProps) string {
	t.Helper()
	var sb strings.Builder
	if err := RatingWidget(p).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render RatingWidget: %v", err)
	}
	return sb.String()
}

// Unrated widget shows both controls and no counts when tallies are zero.
func TestRatingWidget_UnratedNoCounts(t *testing.T) {
	out := renderRating(t, RatingProps{Slug: "p", EntryKey: "ticket:abc", CSRF: "tok"})
	if !strings.Contains(out, `name="rating" value="like"`) {
		t.Errorf("missing 👍 control:\n%s", out)
	}
	if !strings.Contains(out, "hit-dislike") {
		t.Errorf("missing 👎 reason control:\n%s", out)
	}
	if strings.Contains(out, "hit-counts") {
		t.Errorf("zero tallies should render no counts:\n%s", out)
	}
	// Stable id with the colon sanitised for a valid selector.
	if !strings.Contains(out, `id="hit-rating-ticket-abc"`) {
		t.Errorf("missing sanitised widget id:\n%s", out)
	}
}

// Counts render only when positive, next to the controls.
func TestRatingWidget_ShowsPositiveCounts(t *testing.T) {
	out := renderRating(t, RatingProps{Slug: "p", EntryKey: "ticket:abc", Likes: 3, Dislikes: 1, CSRF: "tok"})
	if !strings.Contains(out, "hit-counts") {
		t.Errorf("expected counts block:\n%s", out)
	}
	if !strings.Contains(out, "3") || !strings.Contains(out, "1") {
		t.Errorf("counts should show 3 likes / 1 dislike:\n%s", out)
	}
}

// The rated state is sticky: no buttons, a "rated" marker, and the thanks toast.
func TestRatingWidget_RatedState(t *testing.T) {
	out := renderRating(t, RatingProps{Slug: "p", EntryKey: "ticket:abc", Likes: 1, Rated: true, RatedAs: "like"})
	if !strings.Contains(out, "hit-rated") || !strings.Contains(out, "rated") {
		t.Errorf("rated state missing the marker:\n%s", out)
	}
	if !strings.Contains(out, "Thanks") {
		t.Errorf("rated state missing the thanks toast:\n%s", out)
	}
	if strings.Contains(out, `name="rating" value="like"`) {
		t.Errorf("rated state should not still show the rate buttons:\n%s", out)
	}
}
