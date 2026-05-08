package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	tplog "tickets_please/internal/log"
)

// freshServerWithLogs builds a test server that has the log ring wired into
// the deps and returns a logger that writes records into the ring (and only
// the ring — stderr would just clutter test output).
func freshServerWithLogs(t *testing.T) (*httptest.Server, *http.Client, *slog.Logger) {
	t.Helper()
	ring := tplog.NewRing(50)
	ringLogger := slog.New(tplog.NewRingHandler(ring, nil))

	deps := freshDeps(t)
	deps.Logs = ring
	deps.Logger = ringLogger

	mux := http.NewServeMux()
	Mount(mux, deps)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return srv, client, ringLogger
}

// TestLogs_Page_RendersRecentRecord covers the load-bearing behaviour: a log
// line emitted before the GET shows up on /logs.
func TestLogs_Page_RendersRecentRecord(t *testing.T) {
	srv, client, log := freshServerWithLogs(t)

	const needle = "needle-magic-string-87431"
	log.Info(needle, "kind", "test")

	resp, err := client.Get(srv.URL + "/logs")
	if err != nil {
		t.Fatalf("GET /logs: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, needle) {
		t.Errorf("/logs missing logged needle %q\nbody:\n%s", needle, body)
	}
	if !strings.Contains(body, "Server logs") {
		t.Errorf("/logs missing page heading\n%s", body)
	}
	// htmx polling on the wrapper so the page tails new records in-place.
	if !strings.Contains(body, `hx-get="/logs"`) || !strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Errorf("/logs missing htmx polling attrs:\n%s", body)
	}
}

// TestLogs_HXFragment: an HX-Request to /logs returns just the wrapper
// partial (no full chrome) so the htmx swap doesn't double-render the page.
func TestLogs_HXFragment(t *testing.T) {
	srv, client, log := freshServerWithLogs(t)
	log.Info("hx-fragment-needle", "kind", "test")

	req, _ := http.NewRequest("GET", srv.URL+"/logs", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /logs (HX): %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "hx-fragment-needle") {
		t.Errorf("HX fragment missing needle:\n%s", body)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "Server logs</h1>") {
		t.Errorf("HX fragment leaked chrome:\n%s", body)
	}
	if !strings.Contains(body, `id="logs-wrap"`) {
		t.Errorf("HX fragment missing wrapper id:\n%s", body)
	}
}

// TestLogs_NavLink: the topbar Logs link is visible on every page.
func TestLogs_NavLink(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, `href="/logs"`) {
		t.Errorf("topnav missing Logs link:\n%s", body)
	}
}

// TestLogs_Page_NoRingConfigured: when deps.Logs is nil the page still
// renders 200 with the empty-state hint rather than panicking.
func TestLogs_Page_NoRingConfigured(t *testing.T) {
	srv, client := freshServer(t)
	resp, err := client.Get(srv.URL + "/logs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := mustReadAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "No log records") {
		t.Errorf("/logs missing empty-state copy:\n%s", body)
	}
}

// TestRing_EvictsOldest exercises the wrap-around contract directly so a
// regression there shows up with a clear message.
func TestRing_EvictsOldest(t *testing.T) {
	r := tplog.NewRing(3)
	r.Append([]byte("a"))
	r.Append([]byte("b"))
	r.Append([]byte("c"))
	r.Append([]byte("d")) // evicts a
	got := r.Snapshot()
	want := []string{"b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if string(got[i]) != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestRingHandler_WritesJSON confirms the handler encodes records as JSON
// lines into the ring (load-bearing for the /logs render).
func TestRingHandler_WritesJSON(t *testing.T) {
	r := tplog.NewRing(8)
	h := tplog.NewRingHandler(r, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h)
	logger.Info("hello", "k", "v")
	logger.With("svc", "test").Warn("watchout")

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len = %d, want 2", len(snap))
	}
	first, second := string(snap[0]), string(snap[1])
	if !strings.Contains(first, `"msg":"hello"`) || !strings.Contains(first, `"k":"v"`) {
		t.Errorf("first record missing fields: %q", first)
	}
	if !strings.Contains(second, `"svc":"test"`) || !strings.Contains(second, `"msg":"watchout"`) {
		t.Errorf("second record missing With-attr or msg: %q", second)
	}
}

// TestRingHandler_RaceSafe just makes sure parallel writes don't blow up
// under -race. Numbers are tiny because this is a smoke, not a benchmark.
func TestRingHandler_RaceSafe(t *testing.T) {
	r := tplog.NewRing(64)
	logger := slog.New(tplog.NewRingHandler(r, nil))
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(g int) {
			for i := 0; i < 50; i++ {
				logger.Info("race", "g", g, "i", i)
			}
			done <- struct{}{}
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	// Snapshot under contention shouldn't deadlock or panic.
	_ = r.Snapshot()
	_ = context.Background()
}
