package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
)

// seedProjectAndPhase creates a project + phase via Service so phase
// detail/edit/delete/summary tests have a target without round-tripping
// through the HTML form. Returns the project slug + phase slug.
func seedProjectAndPhase(t *testing.T, deps Deps, projectSlug, phaseName string) (string, string) {
	t.Helper()
	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "test-fixture-"+projectSlug, "test-fixture",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, id)
	if _, err := deps.Service.CreateProject(authed, projectSlug, projectSlug, "test", strings.Repeat("z", 220)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	phase, err := deps.Service.CreatePhase(authed, projectSlug, phaseName, "test", strings.Repeat("y", 220))
	if err != nil {
		t.Fatalf("CreatePhase: %v", err)
	}
	return projectSlug, phase.Slug
}

// TestPhases_IndexEmpty: GET /p/{slug}/phases on a project with no phases
// renders the empty-state hint.
func TestPhases_IndexEmpty(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "p1", "p1")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	resp, err := client.Get(srv.URL + "/p/p1/phases")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "No phases yet") {
		t.Errorf("missing empty-state hint\n%s", body)
	}
}

// TestPhases_IndexPopulated: GET /p/{slug}/phases lists the phases.
func TestPhases_IndexPopulated(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	seedProjectAndPhase(t, deps, "demo", "First Phase")

	resp, err := client.Get(srv.URL + "/p/demo/phases")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "First Phase") {
		t.Errorf("phase name missing from index\n%s", body)
	}
}

// TestPhases_NewForm: GET /p/{slug}/phases/new renders the create form
// with a CSRF token.
func TestPhases_NewForm(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "n", "newform")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	resp, err := client.Get(srv.URL + "/p/newform/phases/new")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{`name="name"`, `name="summary"`, `name="_csrf"`, `action="/p/newform/phases"`} {
		if !strings.Contains(body, want) {
			t.Errorf("new form missing %q", want)
		}
	}
}

// TestPhases_Create_RejectsMissingCSRF: POST without _csrf is 403'd by the
// withCSRF middleware before reaching the handler.
func TestPhases_Create_RejectsMissingCSRF(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "c", "needscsrf")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	form := url.Values{"name": {"X"}, "summary": {strings.Repeat("z", 220)}}
	resp, err := client.PostForm(srv.URL+"/p/needscsrf/phases", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// TestPhases_Create_Happy: POST /p/{slug}/phases creates the phase, sets a
// flash, and 303s to the phase detail page.
func TestPhases_Create_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "h", "happy")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"name":        {"My Phase"},
		"description": {"d"},
		"summary":     {strings.Repeat("z", 220)},
		"_csrf":       {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p/happy/phases", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/p/happy/phases/") {
		t.Errorf("Location = %q, want /p/happy/phases/...", loc)
	}
	hasFlash := false
	for _, c := range resp.Cookies() {
		if c.Name == flashCookieName && c.Value != "" {
			hasFlash = true
		}
	}
	if !hasFlash {
		t.Errorf("no flash cookie emitted")
	}
}

// TestPhases_Create_ShortSummary: server enforces ≥200 chars on summary; UI
// surfaces the validation error inline as 422.
func TestPhases_Create_ShortSummary(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "s", "shortsum")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"name":    {"Phase"},
		"summary": {"too short"},
		"_csrf":   {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p/shortsum/phases", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "form-error") {
		t.Errorf("missing form-error block\n%s", body)
	}
}

// TestPhases_Detail: registered project + phase, GET renders title + danger
// zone + waves section.
func TestPhases_Detail(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, phaseSlug := seedProjectAndPhase(t, deps, "dp", "Detail Phase")

	resp, err := client.Get(srv.URL + "/p/dp/phases/" + phaseSlug)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"Detail Phase", "/p/dp/phases/" + phaseSlug + "/edit", "/p/dp/phases/" + phaseSlug + "/summary", "danger-zone"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}
}

// TestPhases_Detail_NotFound: 404 for unknown phase slug.
func TestPhases_Detail_NotFound(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "nf", "nfproj")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	resp, err := client.Get(srv.URL + "/p/nfproj/phases/no-such-phase")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestPhases_Update: POST /p/{slug}/phases/{phase} with new name updates the
// phase.
func TestPhases_Update(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, phaseSlug := seedProjectAndPhase(t, deps, "up", "Old Name")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"name":        {"Renamed"},
		"description": {"new desc"},
		"_csrf":       {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p/up/phases/"+phaseSlug, form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	got, err := deps.Service.GetPhase(context.Background(), "up", phaseSlug)
	if err != nil {
		t.Fatalf("GetPhase: %v", err)
	}
	if got.Name != "Renamed" {
		t.Errorf("name = %q, want Renamed", got.Name)
	}
}

// TestPhases_Delete_RequiresConfirm: POST without confirm=yes is 400.
func TestPhases_Delete_RequiresConfirm(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, phaseSlug := seedProjectAndPhase(t, deps, "drc", "ToDelete")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"_csrf": {csrf}} // no confirm
	resp, err := client.PostForm(srv.URL+"/p/drc/phases/"+phaseSlug+"/delete", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestPhases_Delete_Happy: POST with confirm=yes removes the phase and 303s
// back to the index.
func TestPhases_Delete_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, phaseSlug := seedProjectAndPhase(t, deps, "dh", "GonnaGo")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"_csrf": {csrf}, "confirm": {"yes"}}
	resp, err := client.PostForm(srv.URL+"/p/dh/phases/"+phaseSlug+"/delete", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/p/dh/phases" {
		t.Errorf("Location = %q, want /p/dh/phases", loc)
	}
	if _, err := deps.Service.GetPhase(context.Background(), "dh", phaseSlug); err == nil {
		t.Errorf("expected phase to be gone after delete")
	}
}

// TestPhases_Summary_HxEdit: GET /p/{slug}/phases/{phase}/summary?edit=1 with
// an HX-Request header returns just the edit partial (no chrome).
func TestPhases_Summary_HxEdit(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, phaseSlug := seedProjectAndPhase(t, deps, "hx", "Summary Phase")

	req, _ := http.NewRequest("GET", srv.URL+"/p/hx/phases/"+phaseSlug+"/summary?edit=1", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "<aside") {
		t.Errorf("HX edit response must be a partial without chrome\n%s", body)
	}
	if !strings.Contains(body, `id="summary-region"`) || !strings.Contains(body, `name="summary"`) {
		t.Errorf("edit partial missing summary form elements\n%s", body)
	}
}

// TestAssignTicketToPhase_RequiresComment: POST /tickets/{id}/assign-phase
// without a comment surfaces the service's "comment required" error.
func TestAssignTicketToPhase_RequiresComment(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, phaseSlug := seedProjectAndPhase(t, deps, "atp", "Target")

	// Need a ticket. Seed one via Service.
	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "atp-agent", "atp",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, id)
	tkt, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
		ProjectIDOrSlug: "atp",
		Title:           "T1",
		Body:            "body",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"phase":   {phaseSlug},
		"comment": {""}, // empty — service rejects
		"_csrf":   {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tkt.ID+"/assign-phase", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("expected non-303, got 303 (assignment should have failed without comment)")
	}
	_ = body
}

// TestAssignTicketToPhase_Happy: with a non-empty comment, the assignment
// succeeds and we 303 to /tickets/{id} with a slug hint preserved.
func TestAssignTicketToPhase_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, phaseSlug := seedProjectAndPhase(t, deps, "ath", "Target")

	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "ath-agent", "ath",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, id)
	tkt, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
		ProjectIDOrSlug: "ath",
		Title:           "T1",
		Body:            "body",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"phase":   {phaseSlug},
		"comment": {"moving to target phase"},
		"_csrf":   {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tkt.ID+"/assign-phase?slug=ath", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/tickets/"+tkt.ID) {
		t.Errorf("Location = %q, want prefix /tickets/%s", loc, tkt.ID)
	}
	if !strings.Contains(loc, "slug=ath") {
		t.Errorf("Location missing slug hint: %q", loc)
	}
}

// TestPhasesIndex_RendersDetailsPerPhase: the new collapsible layout uses
// <details class="phase-row"> per phase rather than a flat <table>.
func TestPhasesIndex_RendersDetailsPerPhase(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	seedProjectAndPhase(t, deps, "demo", "First Phase")

	resp, err := client.Get(srv.URL + "/p/demo/phases")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `<details class="phase-row" data-phase-id=`) {
		t.Errorf("expected a phase-row details element, got:\n%s", body)
	}
	// Default-collapsed (no wave focus): templ renders the open?={false} boolean
	// attribute as nothing, so the summary follows the opening tag with no
	// intervening ` open`.
	if strings.Contains(body, ` open><summary class="phase-row-summary"`) {
		t.Errorf("phase-row should default to collapsed, found open attribute\n%s", body)
	}
}

// TestPhasesIndex_EnrichesWithWaves: with tickets distributed across waves
// in a phase, the phase row's body lists each wave with its tickets and
// keeps tickets from a different phase out of the bucket.
func TestPhasesIndex_EnrichesWithWaves(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, phase1Slug := seedProjectAndPhase(t, deps, "ewp", "Alpha")

	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "ewp-agent", "ewp",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, id)

	// Second phase so we can verify no cross-phase leakage.
	phase2, err := deps.Service.CreatePhase(authed, slug, "Beta", "second", strings.Repeat("y", 220))
	if err != nil {
		t.Fatalf("CreatePhase: %v", err)
	}

	mkTicket := func(title, phaseSlug string, wave int) {
		ps := phaseSlug
		if _, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
			ProjectIDOrSlug: slug,
			Title:           title,
			Body:            "x",
			PhaseIDOrSlug:   &ps,
			Wave:            wave,
		}); err != nil {
			t.Fatalf("CreateTicket %q: %v", title, err)
		}
	}
	// Alpha phase: two tickets in wave 1, one in wave 2.
	mkTicket("alpha-w1-a", phase1Slug, 1)
	mkTicket("alpha-w1-b", phase1Slug, 1)
	mkTicket("alpha-w2", phase1Slug, 2)
	// Beta phase: one ticket in wave 1.
	mkTicket("beta-w1", phase2.Slug, 1)

	resp, err := client.Get(srv.URL + "/p/" + slug + "/phases")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"alpha-w1-a", "alpha-w1-b", "alpha-w2", "beta-w1", "Wave 1", "Wave 2"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
	// Beta's ticket must not appear inside Alpha's phase-row body. We assert
	// this structurally: split the page on </details> and check that whichever
	// chunk contains "Alpha" doesn't also contain "beta-w1".
	chunks := strings.Split(body, "</details>")
	for _, chunk := range chunks {
		if strings.Contains(chunk, ">Alpha<") && strings.Contains(chunk, "beta-w1") {
			t.Errorf("beta-w1 leaked into the Alpha phase row")
		}
	}
}

// seedPhaseWaveTickets creates a project+phase with one ticket in wave 1
// ("ticket-w1") and one in wave 2 ("ticket-w2"), returning (projectSlug,
// phaseSlug). Used by the wave-focus filter tests.
func seedPhaseWaveTickets(t *testing.T, deps Deps, projectSlug string) (string, string) {
	t.Helper()
	slug, phaseSlug := seedProjectAndPhase(t, deps, projectSlug, "Alpha")
	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, projectSlug+"-agent", projectSlug,
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, id)
	for _, tc := range []struct {
		title string
		wave  int
	}{{"ticket-w1", 1}, {"ticket-w2", 2}} {
		ps := phaseSlug
		if _, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
			ProjectIDOrSlug: slug,
			Title:           tc.title,
			Body:            "x",
			PhaseIDOrSlug:   &ps,
			Wave:            tc.wave,
		}); err != nil {
			t.Fatalf("CreateTicket %q: %v", tc.title, err)
		}
	}
	return slug, phaseSlug
}

// TestPhaseDetail_WaveFocusFilter: ?wave=N on phase-detail renders only that
// wave's tickets (server-side filter) plus the "View all waves" escape.
func TestPhaseDetail_WaveFocusFilter(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, phaseSlug := seedPhaseWaveTickets(t, deps, "wff")

	getBody := func(url string) string {
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		return mustReadAll(t, resp)
	}

	// Unfocused: both waves present.
	body := getBody(srv.URL + "/p/" + slug + "/phases/" + phaseSlug)
	if !strings.Contains(body, "ticket-w1") || !strings.Contains(body, "ticket-w2") {
		t.Fatalf("unfocused detail should list both waves\n%s", body)
	}
	// The focusable affordances render on detail.
	if !strings.Contains(body, `id="w1"`) || !strings.Contains(body, "Focus on this wave") {
		t.Errorf("detail missing wave anchor / focus link\n%s", body)
	}

	// Focused on wave 2: only ticket-w2, plus the clear-filter link + banner.
	body = getBody(srv.URL + "/p/" + slug + "/phases/" + phaseSlug + "?wave=2")
	if strings.Contains(body, "ticket-w1") {
		t.Errorf("?wave=2 should hide wave 1's ticket\n%s", body)
	}
	if !strings.Contains(body, "ticket-w2") {
		t.Errorf("?wave=2 should show wave 2's ticket\n%s", body)
	}
	if !strings.Contains(body, "View all waves") || !strings.Contains(body, "wave-focus-banner") {
		t.Errorf("?wave=2 should render the focus banner + clear link\n%s", body)
	}
}

// TestPhasesIndex_WaveFocusFilter: ?wave=N on the index narrows every phase
// body to that wave and renders the rows open with the banner.
func TestPhasesIndex_WaveFocusFilter(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _ := seedPhaseWaveTickets(t, deps, "iwf")

	resp, err := client.Get(srv.URL + "/p/" + slug + "/phases?wave=1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if !strings.Contains(body, "ticket-w1") {
		t.Errorf("?wave=1 should show wave 1's ticket\n%s", body)
	}
	if strings.Contains(body, "ticket-w2") {
		t.Errorf("?wave=1 should hide wave 2's ticket\n%s", body)
	}
	if !strings.Contains(body, "wave-focus-banner") {
		t.Errorf("?wave=1 should render the focus banner\n%s", body)
	}
}

// TestPhasesIndex_UnphasedSection: tickets without a PhaseID surface in the
// "Unphased" pseudo-phase section on the phases index (their old home, the
// board, is gone) — but must NOT leak into any real phase's wave breakdown.
func TestPhasesIndex_UnphasedSection(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _ := seedProjectAndPhase(t, deps, "plx", "Alpha")

	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "plx-agent", "plx",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, id)

	if _, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
		ProjectIDOrSlug: slug,
		Title:           "orphan-ticket",
		Body:            "x",
		Wave:            1,
		// PhaseIDOrSlug intentionally nil
	}); err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	resp, err := client.Get(srv.URL + "/p/" + slug + "/phases")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d\n%s", resp.StatusCode, body)
	}
	// The orphan ticket now appears, inside the Unphased section.
	if !strings.Contains(body, "orphan-ticket") {
		t.Errorf("phase-less ticket missing from phases index:\n%s", body)
	}
	if !strings.Contains(body, `id="unphased"`) {
		t.Errorf("Unphased section not rendered when phase-less tickets exist:\n%s", body)
	}
	if !strings.Contains(body, "unphased ticket") {
		t.Errorf("Unphased count label missing:\n%s", body)
	}
	// It must not have leaked into Alpha's phase-row body. Structural check:
	// the chunk containing "Alpha" must not also contain the orphan ticket.
	chunks := strings.Split(body, "</details>")
	for _, chunk := range chunks {
		if strings.Contains(chunk, ">Alpha<") && strings.Contains(chunk, "orphan-ticket") {
			t.Errorf("orphan-ticket leaked into the Alpha phase row")
		}
	}
}

// TestPhasesIndex_NoUnphasedSectionWhenEmpty: with no phase-less tickets, the
// Unphased section is not rendered at all.
func TestPhasesIndex_NoUnphasedSectionWhenEmpty(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _ := seedProjectAndPhase(t, deps, "ple", "Alpha")

	resp, err := client.Get(srv.URL + "/p/" + slug + "/phases")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, `id="unphased"`) {
		t.Errorf("Unphased section rendered with no phase-less tickets:\n%s", body)
	}
}
