package web

import (
	"context"

	"tickets_please/internal/domain"
	"tickets_please/internal/eventbus"
	phasescomp "tickets_please/internal/web/components/pages/phases"
	"tickets_please/internal/web/sse"
)

// renderPhasePatch turns a ticket-scoped event delivered on a phase: or
// project: topic into the element patches the phases-index and phase-detail
// pages consume. Two inner-morphs per affected phase:
//
//   - #phase-waves-{phaseID}: the wave list (rows, dot colours, badges, plus
//     row insert/removal on create/archive) — handles the common per-column
//     change AND topology changes in one re-render, since phases are small.
//   - #phase-meta-{phaseID}: the mini status bar + active/total counts
//     (index only; the selector is absent on phase-detail, a harmless no-op).
//
// State is re-read authoritatively via the service rather than trusted from
// the event payload, so a patch always reflects committed truth (a
// since-deleted ticket/phase simply yields no patch). Archived tickets drop
// out of the default ListTickets scan, so an archive event naturally removes
// the row and rebalances the bar.
//
// Phase reassignment (AssignTicketToPhase) is intentionally not wired here: it
// would need both the old and new phase to reconcile two wave lists, and it's
// a rare operation — the page reload covers it. Documented as a known gap.
func (a *app) renderPhasePatch(ctx context.Context, ev eventbus.Event) []sse.Event {
	switch ev.Kind {
	case eventbus.KindTicketCreated, eventbus.KindTicketMoved, eventbus.KindTicketCompleted,
		eventbus.KindTicketArchived, eventbus.KindTicketUnarchived:
	default:
		return nil
	}
	if ev.PhaseID == "" {
		return nil // phase-less tickets never appear on the phases pages
	}

	proj, err := a.deps.Service.GetProject(ctx, ev.ProjectID)
	if err != nil {
		return nil
	}
	phase, err := a.deps.Service.GetPhase(ctx, ev.ProjectID, ev.PhaseID)
	if err != nil {
		return nil
	}
	phaseID := ev.PhaseID
	tickets, _, err := a.deps.Service.ListTickets(ctx, domain.ListTicketsInput{
		ProjectIDOrSlug: proj.Slug,
		PhaseIDOrSlug:   &phaseID,
		Limit:           200,
	})
	if err != nil {
		return nil
	}

	// Reuse the index handler's bucketing so the streamed markup is identical
	// to a server-rendered phases page (same dist, same wave order, same rows).
	enriched := bucketTicketsByPhaseAndWave([]*domain.Phase{phase}, tickets)
	if len(enriched) == 0 {
		return nil
	}
	pw := enriched[0]
	// Focusable affordances (the #w{n} anchors + "Focus on this wave →" links)
	// matter on the phase-detail page, which is the dominant live-update
	// surface; rebuild them so a streamed wave re-render doesn't strip them.
	// On the index this also lands focusable markup inside the (collapsed)
	// phase body — harmless: the focus links are relative ?wave=N (valid on the
	// index too) and the bare wave ids only nominally duplicate across phases
	// that happen to live-update in the same session.
	row := phasescomp.PhaseRowProps{
		Phase: phase,
		Waves: toWavePropsFocusable(proj.Slug, pw.Waves),
		Dist: phasescomp.PhaseDist{
			Todo:       pw.Dist.Todo,
			InProgress: pw.Dist.InProgress,
			Testing:    pw.Dist.Testing,
			Done:       pw.Dist.Done,
		},
		Total: pw.Total,
	}

	return []sse.Event{
		sse.PatchElements("#phase-waves-"+phase.ID, sse.ModeInner,
			a.renderComp(ctx, phasescomp.PhaseWaves(proj.Slug, row.Waves))),
		sse.PatchElements("#phase-meta-"+phase.ID, sse.ModeInner,
			a.renderComp(ctx, phasescomp.PhaseMeta(row))),
	}
}
