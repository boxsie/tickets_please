package svc

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/worker"
)

// AssignTicketToPhase moves a ticket between phases (or to phase-less). The
// move is a single atomic StageOp:
//
//  1. RenameDir of the entire ticket dir from its source parent to the new
//     parent (under tickets/ for phase-less, under phases/<NNN>-…/tickets/
//     for phased).
//  2. Write of the updated ticket.yaml with the new PhaseID.
//  3. Write of a system_move comment whose body is "Phase reassignment: → X"
//     plus the agent-supplied comment.
//
// Lives in its own file (rather than tickets.go) to coordinate with T07,
// which is editing tickets.go in parallel.
func (s *Service) AssignTicketToPhase(ctx context.Context, ticketID string, phaseIDOrSlug *string, comment string) (*domain.Ticket, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireNonEmptyTrimmed("ticket id", ticketID); err != nil {
		return nil, err
	}
	if comment == "" {
		return nil, fmt.Errorf("%w: comment required", domain.ErrInvalidArgument)
	}

	st, hostSlug, err := s.hostStoreForTicket(ticketID)
	if err != nil {
		return nil, err
	}
	lp, _, err := s.Cache.Get(ctx, hostSlug)
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

	// Resolve target phase (nil = phase-less).
	var newPhaseID *string
	var newPhaseName string
	var newPhaseSlug string
	var newParentRel string
	if phaseIDOrSlug != nil {
		ph, ok := resolvePhase(lp, *phaseIDOrSlug)
		if !ok {
			return nil, fmt.Errorf("%w: phase %q not found in project %s", domain.ErrNotFound, *phaseIDOrSlug, lp.Project.Slug)
		}
		id := ph.ID
		newPhaseID = &id
		newPhaseName = ph.Name
		newPhaseSlug = ph.Slug
		phaseDirName := fmt.Sprintf("%03d-%s", ph.Number, ph.Slug)
		newParentRel = filepath.Join("phases", phaseDirName, "tickets")
	} else {
		newParentRel = filepath.Join("tickets")
	}

	// No-op short-circuit: if the ticket is already in the target phase
	// (including phase-less → phase-less), reject as InvalidArgument so the
	// caller doesn't accidentally chain a comment on a no-op.
	if samePhase(t.PhaseID, newPhaseID) {
		return nil, fmt.Errorf("%w: ticket already in target phase", domain.ErrInvalidArgument)
	}

	// Find the ticket's current on-disk dir (relative to store root) plus
	// number, so we can compute the new path and the auto-commit caption.
	oldRel, _, err := s.findTicketDir(st, lp.Project.Slug, ticketID)
	if err != nil {
		return nil, err
	}
	dirBase := filepath.Base(oldRel)
	newRel := filepath.Join(newParentRel, dirBase)

	// Re-read the on-disk record so we don't drop fields the cache doesn't
	// model (e.g. CompletedByAgentID set by T07).
	rec := &store.TicketRecord{}
	if err := store.ReadYAML(filepath.Join(st.Root, oldRel, "ticket.yaml"), rec); err != nil {
		return nil, fmt.Errorf("read ticket: %w", err)
	}
	rec.PhaseID = newPhaseID
	rec.UpdatedAt = time.Now()

	yamlBytes, err := store.MarshalYAML(rec)
	if err != nil {
		return nil, err
	}

	// Build the system_move comment. Body prefix announces the reassignment;
	// the user's comment is appended below a blank line so listings render
	// cleanly.
	commentNow := time.Now().UTC()
	commentID := uuid.New()
	target := newPhaseName
	if target == "" {
		target = "none"
	}
	commentBody := fmt.Sprintf("Phase reassignment: → %s\n\n%s", target, comment)
	commentBody = ensureTrailingNewline(commentBody)

	commentRec := &store.CommentRecord{
		ID:              commentID.String(),
		TicketID:        rec.ID,
		Kind:            domain.CommentKindSystemMove,
		AuthorAgentID:   &agent.ID,
		AuthorForUserID: actingForUserID(agent),
		FromColumn:      nil,
		ToColumn:        nil,
		CreatedAt:       commentNow,
	}
	commentBytes, err := store.EncodeMarkdown(commentRec, commentBody)
	if err != nil {
		return nil, fmt.Errorf("encode comment: %w", err)
	}

	shortID := hex.EncodeToString(commentID[:4])
	commentFilename := fmt.Sprintf("%s-%s-%s.md", commentNow.Format(commentTimestampLayout), shortID, string(commentRec.Kind))
	commentRel := filepath.Join(newRel, "comments", commentFilename)

	// Drain pending embed jobs on the mount's worker before the rename:
	// without this an in-flight body/comment sidecar can land at the OLD
	// path mid-rename and recreate the source directory we just emptied.
	// Same pattern DeleteX uses around RemovePath.
	if mount := s.mountForSlug(lp.Project.Slug); mount != nil && mount.Worker != nil {
		mount.Worker.Flush(ctx)
	}

	// Single StageOp: rename dir, then write the updated ticket.yaml AND the
	// new comment file at their post-rename paths. The rename runs first, so
	// the writes land in the new location.
	op, err := st.BeginOp()
	if err != nil {
		return nil, err
	}
	defer op.Abort()
	if err := op.RenameDir(oldRel, newRel); err != nil {
		return nil, err
	}
	if err := op.Write(filepath.Join(newRel, "ticket.yaml"), yamlBytes); err != nil {
		return nil, err
	}
	if err := op.Write(commentRel, commentBytes); err != nil {
		return nil, err
	}
	captionSlug := newPhaseSlug
	if captionSlug == "" {
		captionSlug = "none"
	}
	caption := fmt.Sprintf("reassign ticket %s/%03d to phase %s", lp.Project.Slug, rec.Number, captionSlug)
	if err := op.Commit(ctx, store.LockProject(lp.Project.Slug), agent, caption); err != nil {
		return nil, fmt.Errorf("commit assign ticket to phase: %w", err)
	}

	// Update cached ticket + comments in place. Lock is held above.
	t.PhaseID = newPhaseID
	t.UpdatedAt = rec.UpdatedAt

	dc := &domain.Comment{
		ID:         commentRec.ID,
		TicketID:   commentRec.TicketID,
		Kind:       commentRec.Kind,
		Body:       commentBody,
		FromColumn: nil,
		ToColumn:   nil,
		Author:     hydrateAgentRef(s.AgentStore, agent.ID, agent.Name),
		AuthorFor:  actingForRef(agent),
		CreatedAt:  commentRec.CreatedAt,
	}
	lp.Comments[ticketID] = append(lp.Comments[ticketID], dc)

	// Async embed: the system_move comment for the phase reassignment.
	if mount := s.mountForSlug(lp.Project.Slug); mount != nil && mount.Worker != nil {
		commentAbs := filepath.Join(st.Root, commentRel)
		stem := strings.TrimSuffix(filepath.Base(commentAbs), ".md")
		mount.Worker.Enqueue(worker.Job{
			Kind:        worker.JobComment,
			SourcePath:  commentAbs,
			SidecarPath: filepath.Join(filepath.Dir(commentAbs), stem+".embedding.json"),
			EntryID:     commentRec.ID,
			Owner:       lp.Project.Slug,
			Text:        commentBody,
		})
	}

	out := cloneTicket(t)
	out.BlockedBy = computeBlockedBy(out.DependsOn, lp.Tickets)
	return out, nil
}

// samePhase reports whether two phase id pointers point to the same phase
// (treating both nils as equal — the phase-less area).
func samePhase(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
