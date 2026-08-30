package svc

import (
	"tickets_please/internal/domain"
	"tickets_please/internal/eventbus"
)

// ticketTopics is the fan-out set for a ticket-scoped event: the ticket
// itself, its project, and its phase (when assigned). Phase-detail and
// project pages subscribe to the latter two; the ticket-detail page to the
// first.
func ticketTopics(ticketID, projectID string, phaseID *string) []string {
	topics := make([]string, 0, 3)
	topics = append(topics, eventbus.TopicTicket(ticketID), eventbus.TopicProject(projectID))
	if phaseID != nil && *phaseID != "" {
		topics = append(topics, eventbus.TopicPhase(*phaseID))
	}
	return topics
}

// withActor fills the By* attribution fields on ev from the calling agent and
// (when the agent is acting for a user) that user.
func withActor(ev eventbus.Event, agent *domain.Agent) eventbus.Event {
	ev.ByAgentID = agent.ID
	ev.ByAgentName = agent.Name
	if ref := actingForRef(agent); ref != nil {
		ev.ByUserID = ref.UserID
		ev.ByUserName = ref.DisplayName
	}
	// Fan the actor-attributed event out to the agent's own topic so the
	// /agents/{id} activity feed can stream the agent's ticket/comment events
	// live (#084). Harmless for pages not subscribed to agent:{id}, and the
	// agents-detail renderer only acts on create/complete/comment kinds.
	if agent.ID != "" {
		ev.Topics = append(ev.Topics, eventbus.TopicAgent(agent.ID))
	}
	return ev
}

// derefStr returns the pointed-at string, or "" for a nil pointer.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
