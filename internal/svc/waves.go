package svc

import (
	"context"
	"fmt"
	"sort"

	"tickets_please/internal/domain"
)

// ListWaves returns a per-wave summary over the tickets in scope. The scope
// is determined by phaseIDOrSlug:
//
//   - nil          → wave summary over phase-less tickets
//   - *"<id|slug>" → wave summary over tickets in that phase
//
// Wave 0 is the "unassigned" sentinel and always sorts last so an
// orchestrator naturally walks structured waves first.
func (s *Service) ListWaves(ctx context.Context, projectIDOrSlug string, phaseIDOrSlug *string) ([]domain.WaveSummary, error) {
	lp, _, err := s.Cache.Get(ctx, projectIDOrSlug)
	if err != nil {
		return nil, err
	}
	lp.Lock.RLock()
	defer lp.Lock.RUnlock()

	// Resolve target phase (if any). Nil filter = phase-less tickets only.
	var targetPhaseID *string
	if phaseIDOrSlug != nil {
		ph, ok := resolvePhase(lp, *phaseIDOrSlug)
		if !ok {
			return nil, fmt.Errorf("%w: phase %q in project %s", domain.ErrNotFound, *phaseIDOrSlug, lp.Project.Slug)
		}
		id := ph.ID
		targetPhaseID = &id
	}

	type counters struct {
		total, active int
	}
	buckets := map[int]*counters{}
	for _, t := range lp.Tickets {
		if targetPhaseID == nil {
			if t.PhaseID != nil {
				continue
			}
		} else {
			if t.PhaseID == nil || *t.PhaseID != *targetPhaseID {
				continue
			}
		}
		c, ok := buckets[t.Wave]
		if !ok {
			c = &counters{}
			buckets[t.Wave] = c
		}
		c.total++
		if t.Column != domain.ColumnDone {
			c.active++
		}
	}

	out := make([]domain.WaveSummary, 0, len(buckets))
	for w, c := range buckets {
		out = append(out, domain.WaveSummary{
			Wave:              w,
			TicketCount:       c.total,
			ActiveTicketCount: c.active,
		})
	}
	// Sort: ascending by Wave; wave 0 (unassigned) sorts LAST. Same trick as
	// ticketLess — translate 0 to MaxInt for the sort comparison.
	sort.Slice(out, func(i, j int) bool {
		ai, aj := out[i].Wave, out[j].Wave
		if ai == 0 {
			ai = int(^uint(0) >> 1)
		}
		if aj == 0 {
			aj = int(^uint(0) >> 1)
		}
		return ai < aj
	})
	return out, nil
}
