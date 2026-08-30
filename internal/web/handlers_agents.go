package web

import (
	"net/http"
	"strconv"
	"time"

	agentspg "tickets_please/internal/web/components/pages/agents"
	pgtickets "tickets_please/internal/web/components/pages/tickets"
)

// agentActivityPageSize is how many timeline entries the detail page shows per
// page. The "currently working on" window is a fixed day.
const (
	agentActivityPageSize = 50
	currentWorkWindow     = 24 * time.Hour
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

// handleAgentDetail serves GET /agents/{id} — one agent's identity, a
// "currently working on" callout, and a paginated newest-first activity feed.
// Activity rows reuse the ticket_card / comment partials for visual parity.
func (a *app) handleAgentDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, err := a.deps.Service.GetAgent(r.Context(), id)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}

	page := 0
	if p := r.URL.Query().Get("page"); p != "" {
		if n, perr := strconv.Atoi(p); perr == nil && n > 0 {
			page = n
		}
	}

	// Fetch one past the page boundary so we can tell whether an older page
	// exists without a separate count. AgentActivity materialises the full
	// timeline before truncating, so requesting (page+1)*size+1 yields the
	// newest window we need to slice.
	want := (page+1)*agentActivityPageSize + 1
	items, err := a.deps.Service.AgentActivity(r.Context(), id, want)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}
	hasNext := len(items) > (page+1)*agentActivityPageSize
	start := page * agentActivityPageSize
	if start > len(items) {
		start = len(items)
	}
	end := (page + 1) * agentActivityPageSize
	if end > len(items) {
		end = len(items)
	}
	pageItems := items[start:end]

	work, err := a.deps.Service.AgentCurrentWork(r.Context(), id, time.Now(), currentWorkWindow)
	if err != nil {
		a.renderer.RenderTemplError(w, r, classifyServiceError(err), err)
		return
	}

	currentWork := make([]pgtickets.TicketCardProps, 0, len(work))
	for _, t := range work {
		currentWork = append(currentWork, pgtickets.TicketCardProps{Ticket: t})
	}

	entries := make([]agentspg.ActivityEntry, 0, len(pageItems))
	for _, it := range pageItems {
		e := agentspg.ActivityEntry{
			Kind:        string(it.Kind),
			At:          it.At,
			ProjectSlug: it.ProjectSlug,
		}
		switch it.Kind {
		case "ticket_created", "ticket_completed":
			if it.Ticket != nil {
				e.Card = &pgtickets.TicketCardProps{Ticket: it.Ticket, ProjectSlug: it.ProjectSlug}
			}
		case "comment_added":
			if it.Comment != nil {
				row := toTemplRow(buildCommentRow(it.Comment))
				e.Comment = &row
			}
			if it.Ticket != nil {
				e.TicketID = it.Ticket.ID
				e.TicketTitle = it.Ticket.Title
			}
		}
		entries = append(entries, e)
	}

	props := agentspg.DetailProps{
		Agent:       agentspg.AgentRowFrom(agent),
		CurrentWork: currentWork,
		Activity:    entries,
		Page:        page,
	}
	if page > 0 {
		props.PrevHref = agentDetailPageHref(id, page-1)
	}
	if hasNext {
		props.NextHref = agentDetailPageHref(id, page+1)
	}

	a.renderer.RenderTempl(w, r, PageOpts{
		Title: agent.Name + " · Agents · tickets_please",
	}, agentspg.Detail(props))
}

// agentDetailPageHref builds the activity-feed pagination URL. Page 0 drops the
// query param so the canonical URL stays clean.
func agentDetailPageHref(id string, page int) string {
	if page <= 0 {
		return "/agents/" + id
	}
	return "/agents/" + id + "?page=" + strconv.Itoa(page)
}
