package web

import (
	"context"
	"encoding/json"
	"strings"

	"tickets_please/internal/eventbus"
	"tickets_please/internal/web/sse"
)

// renderDelivery projects one eventbus delivery into the Datastar patch
// frame(s) to write on this connection. Every delivery emits a baseline
// signal frame (so pages can react generically — toasts, optimistic
// reconciliation in #85), and topic-class-specific element patches are layered
// on top by the dispatch below.
//
// Dispatch is keyed by the matched topic's class, not the connected page: a
// TicketMoved delivered on ticket:{id} renders ticket-detail patches, while
// the same event delivered on project:{id} renders phase-row patches. A patch
// whose selector isn't present on the receiving page is a harmless no-op in
// Datastar.
func (a *app) renderDelivery(ctx context.Context, d eventbus.Delivery) []sse.Event {
	frames := []sse.Event{a.signalFrame(d.Event)}

	switch {
	case strings.HasPrefix(d.Topic, "ticket:"):
		frames = append(frames, a.renderTicketPatch(ctx, d.Event)...)
	case strings.HasPrefix(d.Topic, "phase:"), strings.HasPrefix(d.Topic, "project:"):
		frames = append(frames, a.renderPhasePatch(d.Event)...)
	case d.Topic == eventbus.TopicGlobalAgents, strings.HasPrefix(d.Topic, "agent:"):
		frames = append(frames, a.renderAgentPatch(d.Event)...)
	}
	return frames
}

// signalFrame merges a compact description of the event into the client signal
// store under $tpEvent. Pages bind to it for transient toasts and for the
// optimistic-UI reconciliation in #85 (matching a server echo to a pending
// local mutation).
func (a *app) signalFrame(ev eventbus.Event) sse.Event {
	payload := map[string]any{
		"seq":      ev.Seq,
		"kind":     string(ev.Kind),
		"ticketId": ev.TicketID,
		"column":   ev.ToColumn,
		"byAgent":  ev.ByAgentName,
	}
	b, err := json.Marshal(map[string]any{"tpEvent": payload})
	if err != nil {
		return sse.PatchSignals(`{"tpEvent":{}}`)
	}
	return sse.PatchSignals(string(b))
}

// renderPhasePatch produces phases-page element patches. Fleshed out in #83
// (per-row dot/badge swaps, count rebalancing).
func (a *app) renderPhasePatch(ev eventbus.Event) []sse.Event {
	return nil
}

// renderAgentPatch produces /agents page element patches. Fleshed out in #84
// once the agents pages exist (registration insert, last-seen tick). The
// dev-ping smoke probe (handleSSEPing) rides this path to update #sse-target.
func (a *app) renderAgentPatch(ev eventbus.Event) []sse.Event {
	if ev.Kind == eventbus.KindAgentSeen && ev.AgentID == devPingAgentID {
		return []sse.Event{sse.PatchElements("", "", devPingSpan(ev.AgentName))}
	}
	return nil
}
