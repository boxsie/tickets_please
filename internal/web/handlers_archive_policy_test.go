package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// savePolicyForm posts an archive-policy save and returns the response status.
func savePolicy(t *testing.T, srv string, client *http.Client, slug, csrf string, fields url.Values) int {
	t.Helper()
	fields.Set("action", "save")
	fields.Set("_csrf", csrf)
	resp, err := client.PostForm(srv+"/p/"+slug+"/archive-policy", fields)
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestArchivePolicy_SaveRoundTrip: a save persists every knob and the GET form
// reflects it.
func TestArchivePolicy_SaveRoundTrip(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _ := seedProjectAndTicket(t, deps, "ap", "Policy")
	csrf := primeCSRF(t, client, srv.URL)

	status := savePolicy(t, srv.URL, client, slug, csrf, url.Values{
		"enabled":                {"true"},
		"min_age_days":           {"42"},
		"min_retrievals":         {"5"},
		"dislike_ratio":          {"0.3"},
		"early_archive_age_days": {"7"},
		"auto_sweep_on_mount":    {"true"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("save status = %d, want 303", status)
	}

	got, err := deps.Service.GetArchivePolicy(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetArchivePolicy: %v", err)
	}
	if !got.Enabled || got.MinAgeDays != 42 || got.MinRetrievals != 5 ||
		got.DislikeRatio != 0.3 || got.EarlyArchiveAgeDays != 7 || !got.AutoSweepOnMount {
		t.Errorf("policy not persisted: %+v", got)
	}

	// The form reflects the saved values.
	body := getBody(t, client, srv.URL+"/p/"+slug+"/settings")
	if !strings.Contains(body, `value="42"`) {
		t.Errorf("settings form missing saved min_age_days")
	}
	if !strings.Contains(body, "Archive policy") {
		t.Errorf("settings page missing the archive fieldset")
	}
}

// TestArchivePolicy_PreviewDryRun: with permissive thresholds a fresh ticket
// would archive; preview renders it as a dry-run and does NOT flip the flag.
func TestArchivePolicy_PreviewDryRun(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, tid := seedProjectAndTicket(t, deps, "appv", "Sweep Me")
	csrf := primeCSRF(t, client, srv.URL)
	// enabled + zero thresholds → a never-retrieved, unrated ticket qualifies.
	savePolicy(t, srv.URL, client, slug, csrf, url.Values{
		"enabled":        {"true"},
		"min_age_days":   {"0"},
		"min_retrievals": {"0"},
		"dislike_ratio":  {"0.5"},
	})

	form := url.Values{"action": {"preview"}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/p/"+slug+"/archive-policy", form)
	if err != nil {
		t.Fatalf("POST preview: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Dry-run") || !strings.Contains(body, "Sweep Me") {
		t.Errorf("dry-run report missing expected content:\n%s", body)
	}
	if got, _ := deps.Service.GetTicket(context.Background(), tid); got.Archived {
		t.Errorf("dry-run should NOT archive the ticket")
	}
}

// TestArchivePolicy_ApplyWrites: apply actually flips the ticket to archived.
func TestArchivePolicy_ApplyWrites(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, tid := seedProjectAndTicket(t, deps, "apap", "Archive For Real")
	csrf := primeCSRF(t, client, srv.URL)
	savePolicy(t, srv.URL, client, slug, csrf, url.Values{
		"enabled":        {"true"},
		"min_age_days":   {"0"},
		"min_retrievals": {"0"},
		"dislike_ratio":  {"0.5"},
	})

	form := url.Values{"action": {"apply"}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/p/"+slug+"/archive-policy", form)
	if err != nil {
		t.Fatalf("POST apply: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Applied") {
		t.Errorf("apply report missing 'Applied':\n%s", body)
	}
	if got, _ := deps.Service.GetTicket(context.Background(), tid); !got.Archived {
		t.Errorf("apply should archive the ticket")
	}
}

// TestArchivePolicy_PreviewDisabledErrors: previewing a disabled policy surfaces
// the service refusal inline.
func TestArchivePolicy_PreviewDisabledErrors(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _ := seedProjectAndTicket(t, deps, "apdis", "Disabled")
	csrf := primeCSRF(t, client, srv.URL)
	savePolicy(t, srv.URL, client, slug, csrf, url.Values{"min_age_days": {"30"}}) // enabled unchecked

	form := url.Values{"action": {"preview"}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/p/"+slug+"/archive-policy", form)
	if err != nil {
		t.Fatalf("POST preview: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("preview on disabled policy should not be 200")
	}
	if !strings.Contains(body, "disabled") {
		t.Errorf("expected a 'disabled' error message:\n%s", body)
	}
}
