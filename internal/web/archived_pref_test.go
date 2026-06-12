package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"tickets_please/internal/domain"
	"tickets_please/internal/svc"
)

// archivedFixture seeds a project with a phase holding one visible ticket and
// one archived ticket (both in the phase, both phase-less variants reachable
// via the overview). Returns the slug, phase slug, and the two unique titles.
func archivedFixture(t *testing.T, deps Deps) (slug, phaseSlug, visible, archived string) {
	t.Helper()
	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "arx-fixture", "arx-fixture",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, id)
	slug = "arx"
	if _, err := deps.Service.CreateProject(authed, slug, slug, "test", strings.Repeat("z", 220)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	ph, err := deps.Service.CreatePhase(authed, slug, "Phase One", "first phase", strings.Repeat("p", 220))
	if err != nil {
		t.Fatalf("CreatePhase: %v", err)
	}
	phaseSlug = ph.Slug
	visible, archived = "VISIBLEKEEPME", "ZARCHIVEDZ"
	if _, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: visible, PhaseIDOrSlug: &phaseSlug,
	}); err != nil {
		t.Fatalf("CreateTicket visible: %v", err)
	}
	arc, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
		ProjectIDOrSlug: slug, Title: archived, PhaseIDOrSlug: &phaseSlug,
	})
	if err != nil {
		t.Fatalf("CreateTicket archived: %v", err)
	}
	if _, err := deps.Service.ArchiveTicket(authed, arc.ID, "archived for the fixture"); err != nil {
		t.Fatalf("ArchiveTicket: %v", err)
	}
	return slug, phaseSlug, visible, archived
}

// hasCookie reports whether the response sets the named cookie to value v.
func setsCookie(resp *http.Response, name, v string) bool {
	for _, c := range resp.Cookies() {
		if c.Name == name && c.Value == v {
			return true
		}
	}
	return false
}

// TestArchived_Overview_TogglesVisibility: the overview hides archived tickets
// by default and includes them (in-place) when ?include_archived=true, which
// also persists the per-user cookie and flips the toggle to its "on" state.
func TestArchived_Overview_TogglesVisibility(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _, visible, archived := archivedFixture(t, deps)

	// Default: archived hidden, toggle present + off.
	resp, err := client.Get(srv.URL + "/p/" + slug)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if !strings.Contains(body, visible) {
		t.Errorf("overview should always show the visible ticket")
	}
	if strings.Contains(body, archived) {
		t.Errorf("overview should hide archived tickets by default")
	}
	if !strings.Contains(body, "Show archived") {
		t.Errorf("overview missing the archived toggle")
	}
	if strings.Contains(body, `archived-toggle on`) {
		t.Errorf("toggle should be off by default")
	}

	// With the param: archived shown, cookie set, toggle on.
	resp, err = client.Get(srv.URL + "/p/" + slug + "?include_archived=true")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body = mustReadAll(t, resp)
	if !strings.Contains(body, archived) {
		t.Errorf("overview should include archived tickets when ?include_archived=true")
	}
	if !setsCookie(resp, showArchivedCookie, "1") {
		t.Errorf("expected tp_show_archived=1 cookie to be set")
	}
	if !strings.Contains(body, `archived-toggle on`) {
		t.Errorf("toggle should be on when archived shown")
	}
}

// TestArchived_CookiePersists: once set via the param, the cookie keeps
// archived visible on a later plain navigation (no param), and the param can
// turn it back off (clearing the cookie).
func TestArchived_CookiePersists(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _, _, archived := archivedFixture(t, deps)

	// Turn on via param.
	if _, err := client.Get(srv.URL + "/p/" + slug + "?include_archived=true"); err != nil {
		t.Fatalf("GET on: %v", err)
	}
	// Plain navigation — cookie should keep archived visible.
	resp, err := client.Get(srv.URL + "/p/" + slug)
	if err != nil {
		t.Fatalf("GET plain: %v", err)
	}
	if body := mustReadAll(t, resp); !strings.Contains(body, archived) {
		t.Errorf("cookie should keep archived visible without the param")
	}
	// Turn off via param=false — clears the cookie, hides archived.
	resp, err = client.Get(srv.URL + "/p/" + slug + "?include_archived=false")
	if err != nil {
		t.Fatalf("GET off: %v", err)
	}
	body := mustReadAll(t, resp)
	if strings.Contains(body, archived) {
		t.Errorf("include_archived=false should hide archived again")
	}
	if !setsCookie(resp, showArchivedCookie, "") {
		t.Errorf("expected the tp_show_archived cookie to be cleared")
	}
}

// TestArchived_PhaseDetail_TogglesVisibility: the phase-detail wave list honours
// the same param.
func TestArchived_PhaseDetail_TogglesVisibility(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, phaseSlug, visible, archived := archivedFixture(t, deps)

	base := srv.URL + "/p/" + slug + "/phases/" + phaseSlug
	body := getBody(t, client, base)
	if !strings.Contains(body, visible) || strings.Contains(body, archived) {
		t.Errorf("phase detail should show only the visible ticket by default")
	}
	body = getBody(t, client, base+"?include_archived=true")
	if !strings.Contains(body, archived) {
		t.Errorf("phase detail should include archived with the param")
	}
}

func TestArchived_PhaseDetail_AllDonePhaseShowsArchivedByDefault(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)

	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "arxdetaildone-fixture", "arxdetaildone-fixture",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, id)
	slug := "arxdetaildone"
	if _, err := deps.Service.CreateProject(authed, slug, slug, "test", strings.Repeat("z", 220)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	ph, err := deps.Service.CreatePhase(authed, slug, "Done Phase", "all done", strings.Repeat("p", 220))
	if err != nil {
		t.Fatalf("CreatePhase: %v", err)
	}
	phaseSlug := ph.Slug
	visibleTitle := "VISIBLE-DONE-PHASE"
	archivedTitle := "ARCHIVED-DONE-PHASE"
	for _, title := range []string{visibleTitle, archivedTitle} {
		tk, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
			ProjectIDOrSlug: slug,
			Title:           title,
			PhaseIDOrSlug:   &phaseSlug,
		})
		if err != nil {
			t.Fatalf("CreateTicket %q: %v", title, err)
		}
		if _, err := deps.Service.CompleteTicket(authed, tk.ID, "", "", "done phase detail visibility"); err != nil {
			t.Fatalf("CompleteTicket %q: %v", title, err)
		}
		if title == archivedTitle {
			if _, err := deps.Service.ArchiveTicket(authed, tk.ID, "archived after completion"); err != nil {
				t.Fatalf("ArchiveTicket %q: %v", title, err)
			}
		}
	}

	base := srv.URL + "/p/" + slug + "/phases/" + phaseSlug
	body := getBody(t, client, base)
	if !strings.Contains(body, visibleTitle) || !strings.Contains(body, archivedTitle) {
		t.Fatalf("all-done phase detail should show archived tickets by default\n%s", body)
	}
	if !strings.Contains(body, `aria-checked="true"`) {
		t.Fatalf("archived toggle should reflect the auto-included state\n%s", body)
	}

	body = getBody(t, client, base+"?include_archived=false")
	if !strings.Contains(body, visibleTitle) {
		t.Fatalf("explicit hide should keep visible done tickets\n%s", body)
	}
	if strings.Contains(body, archivedTitle) {
		t.Fatalf("explicit hide should still hide archived done tickets\n%s", body)
	}
}

func TestArchived_PhasesIndex_AllDoneArchivedPhaseKeepsDoneBar(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)

	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "arxdone-fixture", "arxdone-fixture",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, id)
	slug := "arxdone"
	if _, err := deps.Service.CreateProject(authed, slug, slug, "test", strings.Repeat("z", 220)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	ph, err := deps.Service.CreatePhase(authed, slug, "Archived Done Phase", "all done", strings.Repeat("p", 220))
	if err != nil {
		t.Fatalf("CreatePhase: %v", err)
	}
	for _, title := range []string{"hidden-done-a", "hidden-done-b"} {
		phaseSlug := ph.Slug
		tk, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
			ProjectIDOrSlug: slug,
			Title:           title,
			PhaseIDOrSlug:   &phaseSlug,
		})
		if err != nil {
			t.Fatalf("CreateTicket %q: %v", title, err)
		}
		if _, err := deps.Service.CompleteTicket(authed, tk.ID, "", "", "done archived progress stays visible"); err != nil {
			t.Fatalf("CompleteTicket %q: %v", title, err)
		}
		if _, err := deps.Service.ArchiveTicket(authed, tk.ID, "archived after completion"); err != nil {
			t.Fatalf("ArchiveTicket %q: %v", title, err)
		}
	}

	resp, err := client.Get(srv.URL + "/p/" + slug + "/phases")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "hidden-done-a") || strings.Contains(body, "hidden-done-b") {
		t.Fatalf("archived ticket titles should stay hidden by default\n%s", body)
	}
	if !strings.Contains(body, "phase-row-bar-done") || !strings.Contains(body, "width: 100%") {
		t.Fatalf("all-done archived phase should render a full done progress bar\n%s", body)
	}
	if strings.Contains(body, "phase-row-bar-empty") {
		t.Fatalf("all-done archived phase must not render the empty progress bar\n%s", body)
	}
}

// TestArchived_Search_ToggleAndCookie: the search page renders the toggle and
// persists the param to the cookie (search-hit matching itself rides the async
// embed worker, so this asserts the param/cookie plumbing, not a live hit).
func TestArchived_Search_ToggleAndCookie(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	slug, _, _, _ := archivedFixture(t, deps)

	resp, err := client.Get(srv.URL + "/p/" + slug + "/search?q=hello&kind=tickets&include_archived=true")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if !strings.Contains(body, "Show archived") {
		t.Errorf("search page missing the archived toggle")
	}
	if !strings.Contains(body, `archived-toggle on`) {
		t.Errorf("search toggle should be on with the param")
	}
	if !setsCookie(resp, showArchivedCookie, "1") {
		t.Errorf("search should persist tp_show_archived=1")
	}
}
