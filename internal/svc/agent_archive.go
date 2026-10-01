package svc

import (
	"context"
	"time"
)

const (
	// agentArchiveGrace is how long an expired agent record stays in the live
	// agents/ dir (and on the /agents page) before the sweep archives it.
	agentArchiveGrace = 24 * time.Hour
	// agentArchiveInterval is the sweep cadence after the boot pass.
	agentArchiveInterval = time.Hour
)

// runAgentArchiver keeps the live agent registry small: every cookieless web
// request and every MCP session mints a record, and before this sweep they
// accumulated forever (53k on serves by 2026-09, enough to push a registration
// walk past the lock timeout). Runs once at boot, then hourly, until ctx ends.
func (s *Service) runAgentArchiver(ctx context.Context) {
	defer s.bgWG.Done()
	t := time.NewTicker(agentArchiveInterval)
	defer t.Stop()
	for {
		s.archiveAgentsOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) archiveAgentsOnce(ctx context.Context) {
	started := time.Now()
	report, err := s.AgentStore.ArchiveExpired(ctx, started, agentArchiveGrace)
	took := time.Since(started)
	if err != nil {
		if ctx.Err() == nil {
			s.Logger.Warn("svc: agent archive sweep failed", "err", err, "archived", report.Archived, "took", took)
		}
		return
	}
	if report.Archived > 0 || report.Indexed > 0 {
		s.Logger.Info("svc: agent archive sweep", "archived", report.Archived, "indexed", report.Indexed, "took", took)
	}
}
