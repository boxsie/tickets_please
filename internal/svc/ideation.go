package svc

// ideation.go: promotion of an idea-kind ticket into a real work ticket.
//
// Ideas (domain.KindIdea) are spitballs parked in the `todo` column and hidden
// from the default work surfaces. The one forward path is promotion: flip the
// kind to `work` IN PLACE — keeping the ticket's id, comments, and embedding
// history — rather than copying it into a new ticket. The mechanism mirrors
// flipArchive in archive.go beat-for-beat (re-read yaml → flip → stage the yaml
// write + a system_promote comment together → commit under the per-project
// flock → apply to the cache → publish).

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/domain"
	"tickets_please/internal/eventbus"
	"tickets_please/internal/store"
	"tickets_please/internal/worker"
)

// PromoteIdea turns an idea into a work ticket in place, writing a required
// `system_promote` audit comment. The ticket keeps its `todo` column (ideas
// already sit there) — only the kind changes, so the promoted ticket
// immediately appears in default list_tickets / search_tickets. When
// phaseIDOrSlug is non-nil and differs from the ticket's current phase, the
// promoted ticket is then moved into that phase via AssignTicketToPhase (a
// second audited step); pass nil to leave the ticket where it is.
func (s *Service) PromoteIdea(ctx context.Context, ticketID, comment string, phaseIDOrSlug *string) (*domain.Ticket, error) {
	promoted, err := s.flipIdeaToWork(ctx, ticketID, comment)
	if err != nil {
		return nil, err
	}

	if phaseIDOrSlug == nil {
		return promoted, nil
	}

	// Only reassign if the target phase differs from the ticket's current one,
	// so a redundant phase arg doesn't trip AssignTicketToPhase's no-op reject.
	_, hostSlug, err := s.hostStoreForTicket(ticketID)
	if err != nil {
		return nil, err
	}
	lp, _, err := s.Cache.Get(ctx, hostSlug)
	if err != nil {
		return nil, err
	}
	var targetPhaseID string
	lp.Lock.RLock()
	ph, ok := resolvePhase(lp, *phaseIDOrSlug)
	if ok {
		targetPhaseID = ph.ID
	}
	lp.Lock.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: phase %q not found in project %s", domain.ErrNotFound, *phaseIDOrSlug, hostSlug)
	}
	if promoted.PhaseID != nil && *promoted.PhaseID == targetPhaseID {
		return promoted, nil // already there
	}

	assigned, err := s.AssignTicketToPhase(ctx, ticketID, phaseIDOrSlug, comment)
	if err != nil {
		return nil, fmt.Errorf("promoted but phase assignment failed: %w", err)
	}
	return assigned, nil
}

// flipIdeaToWork is the in-place kind flip, modelled on flipArchive. It refuses
// any ticket that isn't an idea (ErrFailedPrecondition) so promote_idea can't be
// used to "re-promote" or mangle a work ticket.
func (s *Service) flipIdeaToWork(ctx context.Context, ticketID, comment string) (*domain.Ticket, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireNonEmptyTrimmed("ticket id", ticketID); err != nil {
		return nil, err
	}
	if err := requireNonEmptyTrimmed("comment", comment); err != nil {
		return nil, err
	}

	st, slug, err := s.hostStoreForTicket(ticketID)
	if err != nil {
		return nil, err
	}
	lp, _, err := s.Cache.Get(ctx, slug)
	if err != nil {
		return nil, err
	}

	if err := s.authorizeActingFor(agent, lp.Project.ID, true); err != nil {
		return nil, err
	}

	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	t, ok := lp.Tickets[ticketID]
	if !ok {
		return nil, fmt.Errorf("%w: ticket %s", domain.ErrNotFound, ticketID)
	}
	if t.Kind != domain.KindIdea {
		return nil, fmt.Errorf("%w: ticket %s is not an idea (kind=%s); only ideas can be promoted",
			domain.ErrFailedPrecondition, ticketID, t.Kind.OrWork())
	}

	relTicketDir, ticketNumber, err := s.findTicketDirAndNumber(st, slug, ticketID)
	if err != nil {
		return nil, err
	}

	rec := &store.TicketRecord{}
	absYAML := filepath.Join(st.Root, relTicketDir, "ticket.yaml")
	if err := store.ReadYAML(absYAML, rec); err != nil {
		return nil, fmt.Errorf("read ticket: %w", err)
	}
	now := time.Now()
	// Collapse to the stored work form ("") so a promoted ticket is
	// indistinguishable on disk from a natively-created work ticket.
	rec.Kind = domain.KindWork.Stored()
	rec.UpdatedAt = now
	yamlBytes, err := store.MarshalYAML(rec)
	if err != nil {
		return nil, err
	}

	commentID := uuid.New()
	createdAt := now.UTC()
	cRec := &store.CommentRecord{
		ID:              commentID.String(),
		TicketID:        ticketID,
		Kind:            domain.CommentKindSystemPromote,
		AuthorAgentID:   &agent.ID,
		AuthorForUserID: actingForUserID(agent),
		CreatedAt:       createdAt,
	}
	shortID := hex.EncodeToString(commentID[:4])
	commentFilename := fmt.Sprintf("%s-%s-%s.md",
		createdAt.Format(commentTimestampLayout), shortID, string(cRec.Kind))
	commentBody := ensureTrailingNewline(strings.TrimSpace(comment))
	commentBytes, err := store.EncodeMarkdown(cRec, commentBody)
	if err != nil {
		return nil, fmt.Errorf("encode %s comment: %w", cRec.Kind, err)
	}
	relCommentPath := filepath.Join(relTicketDir, "comments", commentFilename)

	op, err := st.BeginOp()
	if err != nil {
		return nil, err
	}
	defer op.Abort()
	if err := op.Write(filepath.Join(relTicketDir, "ticket.yaml"), yamlBytes); err != nil {
		return nil, err
	}
	if err := op.Write(relCommentPath, commentBytes); err != nil {
		return nil, err
	}
	caption := fmt.Sprintf("promote idea %s/%03d", slug, ticketNumber)
	if err := op.Commit(ctx, store.LockProject(slug), agent, caption); err != nil {
		return nil, fmt.Errorf("commit promote idea: %w", err)
	}

	// Async embed the audit comment so search_comments picks it up.
	if mount := s.mountForSlug(slug); mount != nil && mount.Worker != nil {
		commentAbs := filepath.Join(st.Root, relCommentPath)
		stem := strings.TrimSuffix(filepath.Base(commentAbs), ".md")
		mount.Worker.Enqueue(worker.Job{
			Kind:        worker.JobComment,
			SourcePath:  commentAbs,
			SidecarPath: filepath.Join(filepath.Dir(commentAbs), stem+".embedding.json"),
			EntryID:     cRec.ID,
			Owner:       slug,
			Text:        commentBody,
		})
	}

	// Apply mutations to the cached state.
	t.Kind = domain.KindWork
	t.UpdatedAt = rec.UpdatedAt
	domComment := &domain.Comment{
		ID:        cRec.ID,
		TicketID:  cRec.TicketID,
		Kind:      cRec.Kind,
		Body:      commentBody,
		Author:    hydrateAgentRef(s.AgentStore, agent.ID, agent.Name),
		AuthorFor: actingForRef(agent),
		CreatedAt: cRec.CreatedAt,
	}
	lp.Comments[ticketID] = append(lp.Comments[ticketID], domComment)

	s.publish(withActor(eventbus.Event{
		Kind:        eventbus.KindTicketPromoted,
		Topics:      ticketTopics(ticketID, lp.Project.ID, t.PhaseID),
		TicketID:    ticketID,
		ProjectID:   lp.Project.ID,
		PhaseID:     derefStr(t.PhaseID),
		CommentID:   cRec.ID,
		CommentKind: string(cRec.Kind),
	}, agent))

	cp := cloneTicket(t)
	cp.BlockedBy = computeBlockedBy(cp.DependsOn, lp.Tickets)
	return cp, nil
}
