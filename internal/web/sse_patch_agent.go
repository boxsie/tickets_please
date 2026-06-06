package web

import (
	"context"

	"tickets_please/internal/domain"
	"tickets_please/internal/eventbus"
	agentspg "tickets_please/internal/web/components/pages/agents"
	pgtickets "tickets_please/internal/web/components/pages/tickets"
	"tickets_please/internal/web/sse"
)

// renderAgentPatch turns an agent-registry or actor-attributed event delivered
// on global:agents / agent:{id} into the patches the /agents index and
// /agents/{id} detail pages consume. Patches are keyed by stable selectors, so
// any patch whose target isn't on the receiving page is a harmless no-op:
//
//   - AgentSeen  → morph #agent-lastseen-{id} (the relative <time>); present on
//     both the index row and the detail header.
//   - AgentRegistered → prepend a new row to #agents-tbody (index only) with a
//     one-shot highlight class.
//   - TicketCreated/Completed, CommentAdded → prepend the native activity row to
//     #agent-activity-feed (detail only). These arrive on agent:{actorID}, so
//     only that agent's open detail page receives them.
//
// State is re-read authoritatively via the service rather than trusted from the
// event payload; a since-deleted agent/ticket simply yields no patch.
func (a *app) renderAgentPatch(ctx context.Context, ev eventbus.Event) []sse.Event {
	switch ev.Kind {
	case eventbus.KindAgentSeen:
		// The dev-ping smoke probe rides this path to update #sse-target.
		if ev.AgentID == devPingAgentID {
			return []sse.Event{sse.PatchElements("", "", devPingSpan(ev.AgentName))}
		}
		return a.agentLastSeenPatch(ctx, ev.AgentID)
	case eventbus.KindAgentRegistered:
		return a.agentRegisteredPatch(ctx, ev.AgentID)
	case eventbus.KindTicketCreated, eventbus.KindTicketCompleted, eventbus.KindCommentAdded:
		return a.agentActivityPrependPatch(ctx, ev)
	}
	return nil
}

// agentLastSeenPatch re-renders just the last-seen cell for one agent. The
// selector is shared by the index row (<td>) and the detail header (<dd>), so a
// single patch updates whichever page is open.
func (a *app) agentLastSeenPatch(ctx context.Context, agentID string) []sse.Event {
	agent, err := a.deps.Service.GetAgent(ctx, agentID)
	if err != nil {
		return nil
	}
	row := agentspg.AgentRowFrom(agent)
	return []sse.Event{
		sse.PatchElements("#agent-lastseen-"+agentID, sse.ModeInner,
			a.renderComp(ctx, agentspg.AgentLastSeen(row))),
	}
}

// agentRegisteredPatch prepends the freshly-registered agent's row to the index
// table with a one-shot highlight. No-op on pages without #agents-tbody.
func (a *app) agentRegisteredPatch(ctx context.Context, agentID string) []sse.Event {
	agent, err := a.deps.Service.GetAgent(ctx, agentID)
	if err != nil {
		return nil
	}
	row := agentspg.AgentRowFrom(agent)
	return []sse.Event{
		sse.PatchElements("#agents-tbody", sse.ModePrepend,
			a.renderComp(ctx, agentspg.AgentTableRow(row, true))),
	}
}

// agentActivityPrependPatch prepends one activity row to the detail feed when
// the subscribed agent creates/completes a ticket or posts a comment. The event
// arrives on agent:{actorID}, so it only reaches that agent's detail page; the
// selector is absent everywhere else.
func (a *app) agentActivityPrependPatch(ctx context.Context, ev eventbus.Event) []sse.Event {
	entry, ok := a.activityEntryForEvent(ctx, ev)
	if !ok {
		return nil
	}
	return []sse.Event{
		sse.PatchElements("#agent-activity-feed", sse.ModePrepend,
			a.renderComp(ctx, agentspg.ActivityRow(entry))),
	}
}

// activityEntryForEvent re-reads the authoritative ticket/comment and builds the
// activity row for a live event, mirroring the handler's mapping so the streamed
// markup matches a server-rendered feed row.
func (a *app) activityEntryForEvent(ctx context.Context, ev eventbus.Event) (agentspg.ActivityEntry, bool) {
	proj, err := a.deps.Service.GetProject(ctx, ev.ProjectID)
	if err != nil {
		return agentspg.ActivityEntry{}, false
	}
	tk, err := a.deps.Service.GetTicket(ctx, ev.TicketID)
	if err != nil {
		return agentspg.ActivityEntry{}, false
	}

	entry := agentspg.ActivityEntry{ProjectSlug: proj.Slug}
	switch ev.Kind {
	case eventbus.KindTicketCreated:
		entry.Kind = "ticket_created"
		entry.At = tk.CreatedAt
		entry.Card = &pgtickets.TicketCardProps{Ticket: tk, ProjectSlug: proj.Slug}
	case eventbus.KindTicketCompleted:
		entry.Kind = "ticket_completed"
		if tk.CompletedAt != nil {
			entry.At = *tk.CompletedAt
		} else {
			entry.At = tk.UpdatedAt
		}
		entry.Card = &pgtickets.TicketCardProps{Ticket: tk, ProjectSlug: proj.Slug}
	case eventbus.KindCommentAdded:
		c := a.findComment(ctx, ev.TicketID, ev.CommentID)
		if c == nil {
			return agentspg.ActivityEntry{}, false
		}
		entry.Kind = "comment_added"
		entry.At = c.CreatedAt
		row := toTemplRow(buildCommentRow(c))
		entry.Comment = &row
		entry.TicketID = tk.ID
		entry.TicketTitle = tk.Title
	default:
		return agentspg.ActivityEntry{}, false
	}
	return entry, true
}

// findComment looks up one comment by id in a ticket's thread. Returns nil when
// the ticket or comment is gone (the patch is then skipped).
func (a *app) findComment(ctx context.Context, ticketID, commentID string) *domain.Comment {
	comments, err := a.deps.Service.ListComments(ctx, ticketID)
	if err != nil {
		return nil
	}
	for _, c := range comments {
		if c.ID == commentID {
			return c
		}
	}
	return nil
}
