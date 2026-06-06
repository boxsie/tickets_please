package web

import (
	"net/http"

	agentspg "tickets_please/internal/web/components/pages/agents"
)

// handleAgentsIndex serves GET /agents — the app-global registry of every agent
// that has registered with this server. It's not scoped to a project (no slug
// in the path); svc.ListAgents already returns the rows sorted by last-seen desc
// with per-agent counts attached, so the handler just flattens to display rows.
func (a *app) handleAgentsIndex(w http.ResponseWriter, r *http.Request) {
	agents, err := a.deps.Service.ListAgents(r.Context())
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}

	rows := make([]agentspg.AgentRow, 0, len(agents))
	for _, ag := range agents {
		rows = append(rows, agentspg.AgentRowFrom(ag))
	}

	a.renderer.RenderTempl(w, r, PageOpts{
		Title: "Agents · tickets_please",
	}, agentspg.Index(agentspg.IndexProps{Agents: rows}))
}
