package web

import (
	"bytes"
	"context"
	"fmt"

	"github.com/a-h/templ"

	"tickets_please/internal/domain"
	"tickets_please/internal/eventbus"
	pgtickets "tickets_please/internal/web/components/pages/tickets"
	"tickets_please/internal/web/sse"
)

// renderComp renders a templ component to a string for embedding in an SSE
// patch frame. A render error yields "" — a dropped patch is preferable to a
// half-written frame, and the baseline signal frame still fires.
func (a *app) renderComp(ctx context.Context, comp templ.Component) string {
	var buf bytes.Buffer
	if err := comp.Render(ctx, &buf); err != nil {
		a.deps.Logger.Warn("sse: render patch component", "err", err)
		return ""
	}
	return buf.String()
}

// renderTicketPatch turns a ticket-scoped event into the narrow Datastar
// element patches the ticket-detail page consumes. It re-reads authoritative
// state via the service rather than trusting the event payload, so a patch
// always reflects committed truth (and a since-deleted ticket simply yields no
// patch).
func (a *app) renderTicketPatch(ctx context.Context, ev eventbus.Event) []sse.Event {
	switch ev.Kind {
	case eventbus.KindTicketMoved, eventbus.KindTicketCompleted:
		return a.ticketStatePatches(ctx, ev)
	case eventbus.KindCommentAdded:
		return a.commentAppendPatch(ctx, ev)
	case eventbus.KindTicketArchived, eventbus.KindTicketUnarchived:
		return a.archivedBadgePatch(ctx, ev)
	default:
		return nil
	}
}

// ticketStatePatches re-renders the status badge + action cluster (so e.g.
// completing swaps the buttons to the frozen set) and appends a transient
// toast naming who moved it.
func (a *app) ticketStatePatches(ctx context.Context, ev eventbus.Event) []sse.Event {
	tkt, err := a.deps.Service.GetTicket(ctx, ev.TicketID)
	if err != nil {
		return nil
	}
	props := a.detailPropsForPatch(ctx, tkt)

	frames := []sse.Event{
		sse.PatchElements("", "", a.renderComp(ctx, pgtickets.StatusBadge(tkt.Column))),
		sse.PatchElements("", "", a.renderComp(ctx, pgtickets.PageActions(props))),
	}
	if msg := moveToastMessage(ev); msg != "" {
		frames = append(frames, sse.PatchElements("#ticket-toasts", sse.ModeAppend,
			a.renderComp(ctx, pgtickets.Toast(msg))))
	}
	return frames
}

// commentAppendPatch renders the just-added comment row and appends it to the
// thread. The comment is looked up by id from the ticket's thread so the
// streamed row is byte-identical to a server-rendered one (same author label,
// same time format).
func (a *app) commentAppendPatch(ctx context.Context, ev eventbus.Event) []sse.Event {
	comments, err := a.deps.Service.ListComments(ctx, ev.TicketID)
	if err != nil {
		return nil
	}
	var found *domain.Comment
	for _, c := range comments {
		if c.ID == ev.CommentID {
			found = c
			break
		}
	}
	if found == nil {
		return nil
	}
	row := toTemplRow(buildCommentRow(found))
	var frames []sse.Event
	// Optimistic reconciliation: if the mutation carried a client id, the
	// originating tab rendered a "#comment-pending-{cid}" placeholder. Remove
	// it first so the canonical append replaces it rather than doubling up.
	// Other tabs never had that placeholder, so the remove is a harmless no-op
	// there. (This also undoes the #082 double-render: the comment form no
	// longer htmx-appends, it relies on this echo.)
	if ev.ClientID != "" {
		frames = append(frames, sse.PatchElements("#comment-pending-"+ev.ClientID, sse.ModeRemove, ""))
	}
	frames = append(frames, sse.PatchElements("#comments-list", sse.ModeAppend, a.renderComp(ctx, pgtickets.CommentRow(row))))
	return frames
}

// archivedBadgePatch morphs the archived pill to match the ticket's current
// archived flag.
func (a *app) archivedBadgePatch(ctx context.Context, ev eventbus.Event) []sse.Event {
	tkt, err := a.deps.Service.GetTicket(ctx, ev.TicketID)
	if err != nil {
		return nil
	}
	return []sse.Event{
		sse.PatchElements("", "", a.renderComp(ctx, pgtickets.ArchivedBadge(tkt.Archived))),
	}
}

// detailPropsForPatch builds the minimal DetailProps the StatusBadge /
// PageActions patches need: the ticket, its project, the project's phases (for
// the "Reassign phase" affordance), and the done flag. Best-effort — missing
// project/phases degrade to an empty phase list, never a failed patch.
func (a *app) detailPropsForPatch(ctx context.Context, tkt *domain.Ticket) pgtickets.DetailProps {
	proj, _ := a.deps.Service.GetProject(ctx, tkt.ProjectID)
	var phases []*domain.Phase
	if proj != nil {
		phases, _ = a.deps.Service.ListPhases(ctx, proj.Slug)
	}
	return pgtickets.DetailProps{
		Project:     proj,
		Phases:      phases,
		Ticket:      tkt,
		ProjectSlug: projectSlug(proj),
		IsDone:      tkt.Column == domain.ColumnDone,
	}
}

func projectSlug(p *domain.Project) string {
	if p == nil {
		return ""
	}
	return p.Slug
}

// moveToastMessage builds the transient toast text for a move/complete event.
func moveToastMessage(ev eventbus.Event) string {
	who := ev.ByAgentName
	if ev.ByUserName != "" {
		who = fmt.Sprintf("%s (for %s)", who, ev.ByUserName)
	}
	if who == "" {
		who = "someone"
	}
	if ev.Kind == eventbus.KindTicketCompleted {
		return fmt.Sprintf("Completed by %s", who)
	}
	if ev.ToColumn != "" {
		return fmt.Sprintf("Moved to %s by %s", ev.ToColumn, who)
	}
	return ""
}
