// Package web hosts the browser-facing UI bundled into `tickets_please serve`.
// It mounts onto the same http.ServeMux that already exposes /mcp and /healthz,
// shares the same *svc.Service, and runs in the same process. Localhost-only;
// no auth.
package web

import (
	"log/slog"

	"tickets_please/internal/config"
	"tickets_please/internal/eventbus"
	tplog "tickets_please/internal/log"
	"tickets_please/internal/svc"
)

// Deps is everything Mount needs to wire the web UI. The single instance is
// constructed in cmd/tickets_please/main.go's runServe and passed through.
type Deps struct {
	Service *svc.Service
	Logger  *slog.Logger
	Cfg     config.Config
	// Dev enables on-disk template + static reload. Off in prod (templates
	// served from the embedded FS).
	Dev bool
	// Logs is the in-process log ring backing /logs. nil → /logs renders an
	// empty buffer (tests that don't care about the page leave it nil).
	Logs *tplog.Ring
	// Bus is the typed realtime event hub. svc publishes mutations into it and
	// /sse subscribers fan them out as Datastar patches. nil → the /sse stream
	// still opens and heartbeats, but no app events are delivered.
	Bus *eventbus.Bus
}
