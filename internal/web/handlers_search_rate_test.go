package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"tickets_please/internal/domain"
)

// TestSearchRate_Happy: a 👍 POST lands a like and 303s back (no-JS path).
func TestSearchRate_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, tid := seedProjectAndTicket(t, deps, "rate", "Rate Me")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"entry_key": {"ticket:" + tid},
		"rating":    {"like"},
		"_csrf":     {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p/"+slug+"/search/rate", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	counts, err := deps.Service.FeedbackCounts(context.Background(), slug,
		[]domain.EntryKey{domain.EntryKey("ticket:" + tid)})
	if err != nil {
		t.Fatalf("FeedbackCounts: %v", err)
	}
	if counts[domain.EntryKey("ticket:"+tid)].Likes != 1 {
		t.Errorf("expected 1 like, got %+v", counts)
	}
}

// TestSearchRate_Htmx_RendersRatedWidget: the htmx path swaps in the sticky
// rated widget (with the updated counts) rather than redirecting.
func TestSearchRate_Htmx_RendersRatedWidget(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, tid := seedProjectAndTicket(t, deps, "rateh", "Rate Htmx")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"entry_key": {"ticket:" + tid},
		"rating":    {"dislike"},
		"reason":    {"not relevant"},
		"_csrf":     {csrf},
	}
	req, _ := http.NewRequest("POST", srv.URL+"/p/"+slug+"/search/rate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"hit-rating", "rated", "Thanks"} {
		if !strings.Contains(body, want) {
			t.Errorf("rated widget missing %q\n%s", want, body)
		}
	}
	// The reason rode along; the dislike landed.
	counts, _ := deps.Service.FeedbackCounts(context.Background(), slug,
		[]domain.EntryKey{domain.EntryKey("ticket:" + tid)})
	if counts[domain.EntryKey("ticket:"+tid)].Dislikes != 1 {
		t.Errorf("expected 1 dislike, got %+v", counts)
	}
}

// TestSearchRate_UnknownKey: a rating against a non-existent entry is rejected
// (per-key) and surfaces as a 422.
func TestSearchRate_UnknownKey(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _ := seedProjectAndTicket(t, deps, "ratebad", "Rate Bad")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"entry_key": {"ticket:does-not-exist"},
		"rating":    {"like"},
		"_csrf":     {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p/"+slug+"/search/rate", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

// TestSearchRate_BadRating: a missing/invalid rating is a 400.
func TestSearchRate_BadRating(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, tid := seedProjectAndTicket(t, deps, "ratenr", "Rate No Rating")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"entry_key": {"ticket:" + tid},
		"rating":    {"meh"},
		"_csrf":     {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p/"+slug+"/search/rate", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
