package svc

import (
	"context"
	"fmt"
	"sort"
	"time"

	"tickets_please/internal/cache"
	"tickets_please/internal/domain"
	"tickets_please/internal/store"
)

// ActivityKind tags a single entry in an agent's cross-project timeline.
type ActivityKind string

const (
	ActivityTicketCreated   ActivityKind = "ticket_created"
	ActivityTicketCompleted ActivityKind = "ticket_completed"
	ActivityCommentAdded    ActivityKind = "comment_added"
	ActivityAgentRegistered ActivityKind = "agent_registered"
)

// ActivityItem is one event in an agent's unified, newest-first timeline. It is
// a typed union: Kind selects which payload pointers are populated.
//   - ticket_created / ticket_completed: Ticket set, Comment nil.
//   - comment_added:                     Comment set, Ticket set to the
//     commented ticket (for context + linking).
//   - agent_registered:                  both nil; At is the registration time.
//
// Project{ID,Slug} are always set except for agent_registered (which is not
// scoped to a project). The web layer reuses the ticket_card / comment partials
// off the carried pointers, so the timeline stays visually consistent.
type ActivityItem struct {
	Kind      ActivityKind
	At        time.Time
	ProjectID string
	// ProjectSlug is the human-facing slug for building /p/{slug}/... links.
	ProjectSlug string
	Ticket      *domain.Ticket
	Comment     *domain.Comment
}

// ListAgents returns every registered agent, hydrated, sorted by LastSeenAt
// desc (agents that have never been seen — zero LastSeenAt — sort last, ordered
// among themselves by CreatedAt desc). Each agent carries cheap cross-project
// counts (TicketsCreated / TicketsCompleted / CommentsAuthored) tallied by
// walking the loaded project trees.
//
// The walk runs over the in-memory ProjectCache (Cache.Get is a cached read),
// so repeat calls don't full-walk disk; a dedicated memoised aggregate would be
// the next step if the project count ever grows past the cache's LRU cap.
func (s *Service) ListAgents(ctx context.Context) ([]*domain.Agent, error) {
	byID := make(map[string]*domain.Agent)
	out := make([]*domain.Agent, 0)
	if err := s.AgentStore.WalkAgents(func(rec *store.AgentRecord) error {
		a := rec.ToDomain()
		s.hydrateActingFor(a)
		byID[a.ID] = a
		out = append(out, a)
		return nil
	}); err != nil {
		return nil, err
	}

	// Tally counts across every loaded project. Tickets/comments name the
	// authoring agent by id; an agent with no recorded activity simply keeps
	// its zero counts.
	if err := s.walkLoadedProjects(ctx, func(lp *cache.LoadedProject) error {
		lp.Lock.RLock()
		defer lp.Lock.RUnlock()
		for _, t := range lp.Tickets {
			if t.CreatedBy != nil {
				if a := byID[t.CreatedBy.ID]; a != nil {
					a.TicketsCreated++
				}
			}
			if t.CompletedBy != nil {
				if a := byID[t.CompletedBy.ID]; a != nil {
					a.TicketsCompleted++
				}
			}
		}
		for _, comments := range lp.Comments {
			for _, c := range comments {
				if c.Author != nil {
					if a := byID[c.Author.ID]; a != nil {
						a.CommentsAuthored++
					}
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i], out[j]
		// Zero LastSeenAt (never seen) sinks below any real last-seen.
		zi, zj := ai.LastSeenAt.IsZero(), aj.LastSeenAt.IsZero()
		if zi != zj {
			return zj // i before j when j is the zero one
		}
		if !zi && !ai.LastSeenAt.Equal(aj.LastSeenAt) {
			return ai.LastSeenAt.After(aj.LastSeenAt)
		}
		return ai.CreatedAt.After(aj.CreatedAt)
	})
	return out, nil
}

// AgentActivity returns the agent's unified timeline across all projects,
// newest-first, capped at limit (limit <= 0 applies a default). The timeline
// fuses tickets the agent created or completed, comments it authored, and its
// own registration event.
func (s *Service) AgentActivity(ctx context.Context, agentID string, limit int) ([]ActivityItem, error) {
	if agentID == "" {
		return nil, fmt.Errorf("%w: agent id required", domain.ErrInvalidArgument)
	}
	if limit <= 0 {
		limit = 50
	}

	agent, err := s.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}

	items := make([]ActivityItem, 0)
	if err := s.walkLoadedProjects(ctx, func(lp *cache.LoadedProject) error {
		lp.Lock.RLock()
		defer lp.Lock.RUnlock()
		projID, projSlug := lp.Project.ID, lp.Project.Slug
		for _, t := range lp.Tickets {
			if t.CreatedBy != nil && t.CreatedBy.ID == agentID {
				items = append(items, ActivityItem{
					Kind:        ActivityTicketCreated,
					At:          t.CreatedAt,
					ProjectID:   projID,
					ProjectSlug: projSlug,
					Ticket:      t,
				})
			}
			if t.CompletedBy != nil && t.CompletedBy.ID == agentID && t.CompletedAt != nil {
				items = append(items, ActivityItem{
					Kind:        ActivityTicketCompleted,
					At:          *t.CompletedAt,
					ProjectID:   projID,
					ProjectSlug: projSlug,
					Ticket:      t,
				})
			}
		}
		for ticketID, comments := range lp.Comments {
			for _, c := range comments {
				if c.Author == nil || c.Author.ID != agentID {
					continue
				}
				items = append(items, ActivityItem{
					Kind:        ActivityCommentAdded,
					At:          c.CreatedAt,
					ProjectID:   projID,
					ProjectSlug: projSlug,
					Ticket:      lp.Tickets[ticketID],
					Comment:     c,
				})
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// The agent's own registration anchors the bottom of the timeline.
	items = append(items, ActivityItem{
		Kind: ActivityAgentRegistered,
		At:   agent.CreatedAt,
	})

	sort.SliceStable(items, func(i, j int) bool { return items[i].At.After(items[j].At) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// walkLoadedProjects visits every project's in-memory LoadedProject exactly
// once. It lists projects (deduped by slug across stores) and routes each
// through Cache.Get so the tree is loaded-and-cached. fn must do its own
// locking on lp.Lock.
func (s *Service) walkLoadedProjects(ctx context.Context, fn func(lp *cache.LoadedProject) error) error {
	projects, err := s.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, p := range projects {
		lp, _, err := s.Cache.Get(ctx, p.Slug)
		if err != nil {
			return fmt.Errorf("load project %q: %w", p.Slug, err)
		}
		if err := fn(lp); err != nil {
			return err
		}
	}
	return nil
}
