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

// seedProjectAndTicket creates a project + a single ticket via Service so
// detail/move/complete tests have a target. Returns project slug + ticket id.
func seedProjectAndTicket(t *testing.T, deps Deps, projectSlug, title string) (string, string) {
	t.Helper()
	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "tk-fixture-"+projectSlug, "tk-fixture",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, id)
	if _, err := deps.Service.CreateProject(authed, projectSlug, projectSlug, "test", strings.Repeat("z", 220)); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	tkt, err := deps.Service.CreateTicket(authed, domain.CreateTicketInput{
		ProjectIDOrSlug: projectSlug,
		Title:           title,
		Body:            "test body",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	return projectSlug, tkt.ID
}

// TestBoard_RedirectsToPhases: the retired board URL 302s to /phases so stale
// bookmarks, agent memory, and old comment links don't 404. The test client
// is configured not to follow redirects, so we observe the 302 directly.
func TestBoard_RedirectsToPhases(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	seedProjectAndTicket(t, deps, "brd", "x")

	resp, err := client.Get(srv.URL + "/p/brd/board")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/p/brd/phases" {
		t.Errorf("Location = %q, want /p/brd/phases", got)
	}
}

// TestTicket_NewForm: GET /p/{slug}/tickets/new renders the form.
func TestTicket_NewForm(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "n", "newt")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	resp, err := client.Get(srv.URL + "/p/newt/tickets/new")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{`name="title"`, `name="body"`, `name="wave"`, `name="_csrf"`, `action="/p/newt/tickets"`} {
		if !strings.Contains(body, want) {
			t.Errorf("new form missing %q", want)
		}
	}
}

// TestTicket_Create_Happy: POST /p/{slug}/tickets creates and 303s to
// /tickets/{id}?slug={slug}.
func TestTicket_Create_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	repo := seedRepoOnDisk(t, t.TempDir(), "ch", "createhappy")
	if _, err := deps.Service.RegisterProjectMount(context.Background(), repo); err != nil {
		t.Fatalf("RegisterProjectMount: %v", err)
	}
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"title": {"My Ticket"},
		"body":  {"hi"},
		"wave":  {"0"},
		"_csrf": {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/p/createhappy/tickets", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/tickets/") || !strings.Contains(loc, "?slug=createhappy") {
		t.Errorf("Location = %q, want /tickets/...?slug=createhappy", loc)
	}
}

// TestTicket_Detail: a created ticket renders title, body, badge.
func TestTicket_Detail(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "dt", "Detail Title")

	resp, err := client.Get(srv.URL + "/tickets/" + tid + "?slug=dt")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"Detail Title", "test body", "badge-todo", "Move", "Complete"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}
}

// TestTicket_Move_RequiresComment: POST /tickets/{id}/move with empty
// comment is rejected (422 inline).
func TestTicket_Move_RequiresComment(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "mr", "Move Required")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"target_column": {"in_progress"},
		"comment":       {""},
		"_csrf":         {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/move", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("expected non-303, got 303 (move should reject empty comment)")
	}
}

// TestTicket_Move_Happy: comment provided, redirects, ticket moved.
func TestTicket_Move_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "mh", "Move Happy")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"target_column": {"in_progress"},
		"comment":       {"starting work"},
		"_csrf":         {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/move?slug=mh", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	got, err := deps.Service.GetTicket(context.Background(), tid)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.Column != domain.ColumnInProgress {
		t.Errorf("column = %q, want in_progress", got.Column)
	}
}

// TestTicket_Move_Optimistic_NoContent: POST /move with an Idempotency-Key
// header (the optimistic-JS path) returns 204 and the move still lands; the
// status badge updates via SSE, not the POST response.
func TestTicket_Move_Optimistic_NoContent(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "mopt", "Move Optimistic")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"target_column": {"in_progress"},
		"comment":       {"starting work"},
		"_csrf":         {csrf},
	}
	req, _ := http.NewRequest("POST", srv.URL+"/tickets/"+tid+"/move?slug=mopt", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", "mv-cid-1")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	got, err := deps.Service.GetTicket(context.Background(), tid)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.Column != domain.ColumnInProgress {
		t.Errorf("column = %q, want in_progress", got.Column)
	}
}

// TestTicket_Move_Optimistic_DoneBlocked: the optimistic path also rejects a
// move-to-done, as a plain error status (not the HTML error page).
func TestTicket_Move_Optimistic_DoneBlocked(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "moptd", "Move Optimistic Done")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"target_column": {"done"}, "comment": {"nope"}, "_csrf": {csrf}}
	req, _ := http.NewRequest("POST", srv.URL+"/tickets/"+tid+"/move", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", "mv-cid-2")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "<html") {
		t.Errorf("optimistic error should be plain text, not the HTML error page\n%s", body)
	}
}

// TestTicket_Move_DoneBlocked: target=done is rejected by the handler before
// it even reaches Service.
func TestTicket_Move_DoneBlocked(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "mdb", "Move Done")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"target_column": {"done"},
		"comment":       {"trying to skip complete"},
		"_csrf":         {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/move", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Complete") {
		t.Errorf("error must point user at Complete form\n%s", body)
	}
}

// TestTicket_Complete_Happy: 3 fields ≥10 chars each, ticket lands in done.
func TestTicket_Complete_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "ch", "Complete Happy")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"testing_evidence": {"ran the unit tests and they passed"},
		"work_summary":     {"changed the things in the place"},
		"learnings":        {"watch for the gotcha next time"},
		"_csrf":            {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/complete?slug=ch", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	got, err := deps.Service.GetTicket(context.Background(), tid)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.Column != domain.ColumnDone {
		t.Errorf("column = %q, want done", got.Column)
	}
}

// TestTicket_Complete_TooShort: server enforces minlength on each field.
func TestTicket_Complete_TooShort(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "ct", "Complete Too Short")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"testing_evidence": {"ok"},
		"work_summary":     {"ok"},
		"learnings":        {"ok"},
		"_csrf":            {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/complete", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("expected non-303, got 303 (server should enforce minlength)")
	}
}

// TestTicket_FrozenAfterComplete: detail page on a done ticket renders the
// frozen actions (no Move/Complete forms).
func TestTicket_FrozenAfterComplete(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "fa", "Frozen After")

	// Complete it directly via Service.
	ctx := context.Background()
	aid, _, err := deps.Service.RegisterAgent(ctx, "fa-completer", "fa", map[string]string{"client_name": "t"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, aid)
	if _, err := deps.Service.CompleteTicket(authed, tid,
		"tested manually with curl",
		"changed the handler to do X",
		"learning about Y for next time",
	); err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}

	resp, err := client.Get(srv.URL + "/tickets/" + tid)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "frozen") {
		t.Errorf("done detail must surface frozen state\n%s", body)
	}
	if strings.Contains(body, `name="testing_evidence"`) {
		t.Errorf("done detail must NOT render the complete form")
	}
}

// TestTicket_EditDoneRefused: GET /tickets/{id}/edit on a done ticket
// returns 422 inline.
func TestTicket_EditDoneRefused(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "edr", "Edit Done")

	ctx := context.Background()
	aid, _, err := deps.Service.RegisterAgent(ctx, "edr-c", "edr", map[string]string{"client_name": "t"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	authed := svc.WithSessionID(ctx, aid)
	if _, err := deps.Service.CompleteTicket(authed, tid,
		"tested manually",
		"changed the things",
		"learned a lesson",
	); err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}

	resp, err := client.Get(srv.URL + "/tickets/" + tid + "/edit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
}

// TestTicket_Delete_Happy: POST /tickets/{id}/delete on a non-done ticket
// removes it and 303s to the project phases page.
func TestTicket_Delete_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "del", "Doomed Ticket")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/delete?slug=del", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/p/del/phases" {
		t.Errorf("Location = %q, want /p/del/phases", got)
	}
	if _, err := deps.Service.GetTicket(context.Background(), tid); err == nil {
		t.Error("ticket still resolves after delete; want ErrNotFound")
	}
}

// TestTicket_Detail_ShowsDeleteButton: a non-done ticket detail page renders
// the Delete trigger + dialog.
func TestTicket_Detail_ShowsDeleteButton(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "ddl", "Has Delete")
	resp, err := client.Get(srv.URL + "/tickets/" + tid + "?slug=ddl")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for _, want := range []string{`data-dialog="dlg-delete"`, `id="dlg-delete"`, "Delete forever", `action="/tickets/` + tid + `/delete?slug=ddl"`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}
}

// TestTicket_Update: edit form POST changes title.
func TestTicket_Update(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "up", "Old")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{
		"title": {"New Title"},
		"body":  {"new body"},
		"wave":  {"1"},
		"_csrf": {csrf},
	}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"?slug=up", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	got, err := deps.Service.GetTicket(context.Background(), tid)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.Title != "New Title" {
		t.Errorf("title = %q, want New Title", got.Title)
	}
	if got.Wave != 1 {
		t.Errorf("wave = %d, want 1", got.Wave)
	}
}

// --- archive / unarchive --------------------------------------------------

// getBody GETs a URL and returns the response body as a string, failing the
// test on a transport error.
func getBody(t *testing.T, client *http.Client, urlStr string) string {
	t.Helper()
	resp, err := client.Get(urlStr)
	if err != nil {
		t.Fatalf("GET %s: %v", urlStr, err)
	}
	return mustReadAll(t, resp)
}

// archiveTicket is a small helper that archives a freshly-seeded ticket
// directly via the service so unarchive/detail tests have an archived target
// without re-POSTing through the handler each time.
func archiveTicket(t *testing.T, deps Deps, ticketID string) {
	t.Helper()
	ctx := context.Background()
	id, _, err := deps.Service.RegisterAgent(ctx, "arch-fixture-"+ticketID, "arch-fixture",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if _, err := deps.Service.ArchiveTicket(svc.WithSessionID(ctx, id), ticketID, "seed archive"); err != nil {
		t.Fatalf("ArchiveTicket: %v", err)
	}
}

// TestTicket_Archive_Happy: POST /archive with a comment flips the flag and
// 303s back to the detail page.
func TestTicket_Archive_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "arch", "Archive Me")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"comment": {"no longer relevant"}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/archive?slug=arch", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	got, err := deps.Service.GetTicket(context.Background(), tid)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if !got.Archived {
		t.Errorf("ticket should be archived")
	}
}

// TestTicket_Archive_RequiresComment: the service rejects an empty comment;
// the handler surfaces it as a 422 (the modal also enforces `required`).
func TestTicket_Archive_RequiresComment(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "archnc", "Archive No Comment")
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"comment": {""}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/archive?slug=archnc", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	got, _ := deps.Service.GetTicket(context.Background(), tid)
	if got.Archived {
		t.Errorf("ticket should not be archived after a rejected request")
	}
}

// TestTicket_Archive_AlreadyArchived: archiving an already-archived ticket is
// a precondition failure → 422.
func TestTicket_Archive_AlreadyArchived(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "archdup", "Archive Twice")
	archiveTicket(t, deps, tid)
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"comment": {"again"}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/archive?slug=archdup", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

// TestTicket_Unarchive_Happy: POST /unarchive flips an archived ticket back.
func TestTicket_Unarchive_Happy(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "unarch", "Unarchive Me")
	archiveTicket(t, deps, tid)
	csrf := primeCSRF(t, client, srv.URL)
	form := url.Values{"comment": {"back in play"}, "_csrf": {csrf}}
	resp, err := client.PostForm(srv.URL+"/tickets/"+tid+"/unarchive?slug=unarch", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	got, err := deps.Service.GetTicket(context.Background(), tid)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.Archived {
		t.Errorf("ticket should be unarchived")
	}
}

// TestTicket_Detail_ArchiveButtonAndBadge: a non-archived ticket shows the
// Archive button and the (empty) archived-badge slot; once archived, the
// detail page shows the archived pill and flips the button to Unarchive. The
// archive/unarchive modals are present in both states.
func TestTicket_Detail_ArchiveButtonAndBadge(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "archdet", "Archive Detail")

	body := getBody(t, client, srv.URL+"/tickets/"+tid+"?slug=archdet")
	for _, want := range []string{`data-dialog="dlg-archive"`, `>Archive<`, `id="dlg-archive"`, `id="dlg-unarchive"`} {
		if !strings.Contains(body, want) {
			t.Errorf("active ticket detail missing %q", want)
		}
	}
	if strings.Contains(body, `class="badge badge-archived"`) {
		t.Errorf("active ticket should not show the archived pill")
	}

	archiveTicket(t, deps, tid)
	body = getBody(t, client, srv.URL+"/tickets/"+tid+"?slug=archdet")
	for _, want := range []string{`badge badge-archived`, `>archived<`, `data-dialog="dlg-unarchive"`, `>Unarchive<`} {
		if !strings.Contains(body, want) {
			t.Errorf("archived ticket detail missing %q", want)
		}
	}
}

// TestTicket_Detail_ArchiveAvailableWhenDone: completion freezes the edit/move
// /complete buttons, but archive stays available on a done ticket.
func TestTicket_Detail_ArchiveAvailableWhenDone(t *testing.T) {
	srv, client, deps := freshServerWithDeps(t)
	_, tid := seedProjectAndTicket(t, deps, "archdone", "Archive Done")
	ctx := context.Background()
	aid, _, err := deps.Service.RegisterAgent(ctx, "done-fixture", "done-fixture",
		map[string]string{"client_name": "test"}, 5*time.Minute, "")
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if _, err := deps.Service.CompleteTicket(svc.WithSessionID(ctx, aid), tid,
		"tested it", "did the work", "learned a lot here"); err != nil {
		t.Fatalf("CompleteTicket: %v", err)
	}
	body := getBody(t, client, srv.URL+"/tickets/"+tid+"?slug=archdone")
	if !strings.Contains(body, "frozen") {
		t.Errorf("done ticket should show the frozen-actions badge")
	}
	if !strings.Contains(body, `data-dialog="dlg-archive"`) {
		t.Errorf("done ticket should still offer Archive")
	}
}
