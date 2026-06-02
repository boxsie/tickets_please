package svc

// archive.go: ticket archive / unarchive mutations + the policy sweep.
//
// Archive is independent of column — a `done` ticket stays frozen for
// completion-field edits, but its `archived` flag can flip with a fresh
// audit comment (system_archive / system_unarchive). Flipping is the only
// mutation allowed on done tickets after CompleteTicket.
//
// The default search / list surface excludes archived tickets via post-filter;
// the vec index entries stay in place so unarchive is free (no re-embed).

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/worker"
)

// ArchiveTicket flips the ticket's `archived` flag to true, stamps
// ArchivedAt, and writes a `system_archive` comment with the supplied
// rationale. Refuses on an already-archived ticket (use UnarchiveTicket to
// undo).
//
// Done tickets are explicitly allowed — the freeze rule covers completion
// fields, not the archived flag.
func (s *Service) ArchiveTicket(ctx context.Context, ticketID, comment string) (*domain.Ticket, error) {
	return s.flipArchive(ctx, ticketID, comment, true)
}

// UnarchiveTicket flips the ticket back to active. Refuses on a ticket that
// isn't archived in the first place — no-op archive flips would just bloat
// the audit trail.
func (s *Service) UnarchiveTicket(ctx context.Context, ticketID, comment string) (*domain.Ticket, error) {
	return s.flipArchive(ctx, ticketID, comment, false)
}

// flipArchive is the shared implementation of Archive/Unarchive. Pattern
// mirrors MoveTicket: re-read ticket.yaml off disk (so forward-compat fields
// aren't dropped), flip the flag, stage the yaml write + system comment
// together, commit under the per-project flock, then apply to the cache.
func (s *Service) flipArchive(ctx context.Context, ticketID, comment string, wantArchived bool) (*domain.Ticket, error) {
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
	if wantArchived && t.Archived {
		return nil, fmt.Errorf("%w: ticket %s is already archived (use unarchive_ticket to undo)",
			domain.ErrFailedPrecondition, ticketID)
	}
	if !wantArchived && !t.Archived {
		return nil, fmt.Errorf("%w: ticket %s is not archived",
			domain.ErrFailedPrecondition, ticketID)
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
	rec.Archived = wantArchived
	if wantArchived {
		stamped := now.UTC()
		rec.ArchivedAt = &stamped
	} else {
		rec.ArchivedAt = nil
	}
	rec.UpdatedAt = now
	yamlBytes, err := store.MarshalYAML(rec)
	if err != nil {
		return nil, err
	}

	kind := domain.CommentKindSystemArchive
	captionVerb := "archive"
	if !wantArchived {
		kind = domain.CommentKindSystemUnarchive
		captionVerb = "unarchive"
	}
	commentID := uuid.New()
	createdAt := now.UTC()
	cRec := &store.CommentRecord{
		ID:              commentID.String(),
		TicketID:        ticketID,
		Kind:            kind,
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
		return nil, fmt.Errorf("encode %s comment: %w", kind, err)
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
	caption := fmt.Sprintf("%s ticket %s/%03d", captionVerb, slug, ticketNumber)
	if err := op.Commit(ctx, store.LockProject(slug), agent, caption); err != nil {
		return nil, fmt.Errorf("commit %s ticket: %w", captionVerb, err)
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
	t.Archived = wantArchived
	t.ArchivedAt = rec.ArchivedAt
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

	cp := cloneTicket(t)
	cp.BlockedBy = computeBlockedBy(cp.DependsOn, lp.Tickets)
	return cp, nil
}

// ApplyPolicyInput is the request shape for ApplyArchivePolicy.
type ApplyPolicyInput struct {
	ProjectIDOrSlug string
	// Commit defaults false (dry-run). True actually flips the flags.
	Commit bool
	// Limit caps how many tickets a single call will archive — defaults to
	// applyPolicyDefaultLimit if zero, never exceeds applyPolicyHardCap.
	Limit int
}

// ApplyPolicyEntry is one row in the sweep report.
type ApplyPolicyEntry struct {
	TicketID string
	Title    string
	Reason   string
}

// ApplyPolicyReport is the response body of ApplyArchivePolicy.
type ApplyPolicyReport struct {
	Considered   int
	WouldArchive []ApplyPolicyEntry
	Archived     []ApplyPolicyEntry
	Skipped      []ApplyPolicyEntry
	Config       ArchivePolicy
}

const (
	applyPolicyDefaultLimit = 500
	applyPolicyHardCap      = 5000
)

// ApplyArchivePolicy walks the project's tickets, evaluates each against the
// per-project ArchivePolicy + its feedback record, and returns a dry-run or
// commit report. Refuses when the project's policy isn't enabled (the
// archive.enabled gate is opt-in for a reason — silent archival would be
// a foot-gun without explicit consent).
func (s *Service) ApplyArchivePolicy(ctx context.Context, in ApplyPolicyInput) (*ApplyPolicyReport, error) {
	if _, _, err := s.requireSession(ctx); err != nil {
		return nil, err
	}
	if in.ProjectIDOrSlug == "" {
		return nil, fmt.Errorf("%w: project_id_or_slug required", domain.ErrInvalidArgument)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = applyPolicyDefaultLimit
	}
	if limit > applyPolicyHardCap {
		limit = applyPolicyHardCap
	}
	lp, _, err := s.Cache.Get(ctx, in.ProjectIDOrSlug)
	if err != nil {
		return nil, err
	}
	slug := lp.Project.Slug
	mount := s.mountForSlug(slug)
	if mount == nil {
		return nil, fmt.Errorf("%w: project %q not mounted", domain.ErrFailedPrecondition, slug)
	}
	policy := mount.ArchivePolicy
	if !policy.Enabled {
		return nil, fmt.Errorf("%w: archive policy disabled for project %q — set `archive.enabled: true` in project.yaml",
			domain.ErrFailedPrecondition, slug)
	}

	report := &ApplyPolicyReport{Config: policy}
	now := time.Now().UTC()

	// Snapshot the candidate set under the cache read lock; running Decide on
	// every ticket is cheap (pure function), and we want to release the lock
	// before calling ArchiveTicket below (which takes the cache WRITE lock).
	type candidate struct {
		id    string
		title string
		dec   ArchiveDecision
	}
	var candidates []candidate
	lp.Lock.RLock()
	for id, t := range lp.Tickets {
		report.Considered++
		var fb domain.FeedbackRecord
		if mount.Feedback != nil {
			if rec, ok := mount.Feedback.Get(domain.TicketEntryKey(id)); ok {
				fb = rec
			}
			// Learning + comment feedback could also influence the parent
			// ticket's archive decision in the future. For now we score on the
			// ticket's own aggregate, mirroring the W2 multiplier scope.
		}
		dec := Decide(t, fb, policy, now)
		if !dec.Archive {
			if t.Archived {
				report.Skipped = append(report.Skipped, ApplyPolicyEntry{
					TicketID: id, Title: t.Title, Reason: "already archived",
				})
			}
			continue
		}
		candidates = append(candidates, candidate{id: id, title: t.Title, dec: dec})
	}
	lp.Lock.RUnlock()

	// Sort by id so the dry-run output is deterministic.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for _, c := range candidates {
		report.WouldArchive = append(report.WouldArchive, ApplyPolicyEntry{
			TicketID: c.id, Title: c.title, Reason: c.dec.Reason,
		})
	}

	if !in.Commit {
		return report, nil
	}

	for _, c := range candidates {
		comment := "Archived by policy: " + c.dec.Reason
		if _, err := s.ArchiveTicket(ctx, c.id, comment); err != nil {
			// Race: someone else archived it (or deleted it) between snapshot
			// and apply. Record as skipped rather than fail the whole sweep.
			report.Skipped = append(report.Skipped, ApplyPolicyEntry{
				TicketID: c.id, Title: c.title, Reason: "apply failed: " + err.Error(),
			})
			continue
		}
		report.Archived = append(report.Archived, ApplyPolicyEntry{
			TicketID: c.id, Title: c.title, Reason: c.dec.Reason,
		})
	}
	return report, nil
}
