package svc

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"tickets_please/internal/domain"
	"tickets_please/internal/store"
	"tickets_please/internal/worker"
)

// commentTimestampLayout is the sortable, nanosecond-precision UTC layout used
// in comment filenames so `ls`-order matches creation order. SPEC §Data layout
// pins the format; the 9-digit nanos guarantee distinct filenames even when
// two CreateComment calls land in the same second.
const commentTimestampLayout = "20060102T150405.000000000Z"

// CreateComment writes a free-form user comment to a ticket. Comments are
// immutable per SPEC §Design decisions, so this is the only mutation path for
// user-authored comments — there is no Update or Delete.
//
// Lock ordering (T04 contract): LoadedProject.Lock → StageOp.Commit's flock.
// We take the project write lock, build the StageOp, then Commit (which itself
// acquires the per-project flock). Never invert.
func (s *Service) CreateComment(ctx context.Context, ticketID, body string) (*domain.Comment, error) {
	ctx, agent, err := s.requireSession(ctx)
	if err != nil {
		return nil, err
	}

	if err := requireNonEmptyTrimmed("comment body", body); err != nil {
		return nil, err
	}
	if err := requireNonEmptyTrimmed("ticket id", ticketID); err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(body)

	st, slug, err := s.hostStoreForTicket(ticketID)
	if err != nil {
		return nil, err
	}

	lp, _, err := s.Cache.Get(ctx, slug)
	if err != nil {
		return nil, err
	}

	lp.Lock.Lock()
	defer lp.Lock.Unlock()

	// Defensive re-check after the lock — the project could have been
	// evicted+reloaded between hostStoreForTicket and Cache.Get, or the
	// ticket could have been deleted by a concurrent process (T05/T07).
	if _, ok := lp.Tickets[ticketID]; !ok {
		return nil, fmt.Errorf("%w: ticket %q", domain.ErrNotFound, ticketID)
	}

	// Build the comment record. Frontmatter mirrors SPEC §Data layout.
	now := time.Now().UTC()
	commentID := uuid.New()
	rec := &store.CommentRecord{
		ID:            commentID.String(),
		TicketID:      ticketID,
		Kind:          domain.CommentKindUser,
		AuthorAgentID: &agent.ID,
		FromColumn:    nil,
		ToColumn:      nil,
		CreatedAt:     now,
	}

	// Filename: <ts>-<short-id>-<kind>.md.
	shortID := hex.EncodeToString(commentID[:4])
	filename := fmt.Sprintf("%s-%s-%s.md", now.Format(commentTimestampLayout), shortID, string(rec.Kind))

	// Resolve the on-disk ticket dir + number for the auto-commit caption.
	// The ticket's PhaseID determines whether it lives at
	// projects/<slug>/tickets/ or projects/<slug>/phases/<NNN>-<phase-slug>/tickets/.
	// domain.Ticket doesn't carry Number (the spec keeps that off the
	// hydrated type), so we read it off the disk record in the same walk.
	relTicketDir, ticketNumber, err := s.findTicketDirAndNumber(st, slug, ticketID)
	if err != nil {
		return nil, err
	}
	relCommentPath := filepath.Join(relTicketDir, "comments", filename)

	bodyOut := ensureTrailingNewline(trimmed)
	commentBytes, err := store.EncodeMarkdown(rec, bodyOut)
	if err != nil {
		return nil, fmt.Errorf("encode comment: %w", err)
	}

	op, err := st.BeginOp()
	if err != nil {
		return nil, err
	}
	defer op.Abort()
	if err := op.Write(relCommentPath, commentBytes); err != nil {
		return nil, err
	}
	caption := fmt.Sprintf("comment on %s/%03d", slug, ticketNumber)
	if err := op.Commit(ctx, store.LockProject(slug), agent, caption); err != nil {
		return nil, fmt.Errorf("commit create comment: %w", err)
	}

	// Async embed: the comment body → mount's CommentsIdx.
	if mount := s.mountForSlug(slug); mount != nil && mount.Worker != nil {
		commentAbs := filepath.Join(st.Root, relCommentPath)
		stem := strings.TrimSuffix(filepath.Base(commentAbs), ".md")
		mount.Worker.Enqueue(worker.Job{
			Kind:        worker.JobComment,
			SourcePath:  commentAbs,
			SidecarPath: filepath.Join(filepath.Dir(commentAbs), stem+".embedding.json"),
			EntryID:     rec.ID,
			Owner:       slug,
			Text:        bodyOut,
		})
	}

	// Hydrate the in-memory comment, append to the cache, and return a copy
	// to the caller. Author is best-effort: any agent-lookup failure leaves
	// Author as a thin {ID-only} ref so the audit trail still references the
	// agent id.
	// Store the body exactly as the loader will read it back from disk so a
	// freshly-created Comment compares equal to one round-tripped through
	// the file. EncodeMarkdown ensures a single trailing newline; the loader
	// strips one newline after the closing fence and returns the rest
	// verbatim.
	domComment := &domain.Comment{
		ID:         rec.ID,
		TicketID:   rec.TicketID,
		Kind:       rec.Kind,
		Body:       bodyOut,
		FromColumn: rec.FromColumn,
		ToColumn:   rec.ToColumn,
		Author:     hydrateAgentRef(s.AgentStore, agent.ID, agent.Name),
		CreatedAt:  rec.CreatedAt,
	}

	lp.Comments[ticketID] = append(lp.Comments[ticketID], domComment)

	cp := *domComment
	return &cp, nil
}

// ListComments returns every comment on a ticket — user, system_move, and
// system_completion alike, in chronological order. Read-only; does not require
// a session per SPEC §Agent identity & sessions (read methods are
// unattributed).
func (s *Service) ListComments(ctx context.Context, ticketID string) ([]*domain.Comment, error) {
	if err := requireNonEmptyTrimmed("ticket id", ticketID); err != nil {
		return nil, err
	}

	_, slug, err := s.hostStoreForTicket(ticketID)
	if err != nil {
		return nil, err
	}

	lp, _, err := s.Cache.Get(ctx, slug)
	if err != nil {
		return nil, err
	}

	lp.Lock.RLock()
	defer lp.Lock.RUnlock()

	if _, ok := lp.Tickets[ticketID]; !ok {
		return nil, fmt.Errorf("%w: ticket %q", domain.ErrNotFound, ticketID)
	}

	src := lp.Comments[ticketID]
	out := make([]*domain.Comment, 0, len(src))
	for _, c := range src {
		// Copy so callers can't mutate the cached slice's elements without
		// the project lock.
		cp := *c
		// Re-hydrate Author: the loader only touches it once, but if the
		// agent file was just-written between load and read the cached
		// AgentRef may be stale-without-name. Best-effort refresh; failure
		// keeps whatever the loader put in (possibly nil).
		if cp.Author != nil && cp.Author.Name == "" && cp.Author.ID != "" {
			if r := hydrateAgentRef(s.AgentStore, cp.Author.ID, ""); r != nil {
				cp.Author = r
			}
		}
		out = append(out, &cp)
	}
	return out, nil
}

// findTicketDirAndNumber returns the ticket's directory path relative to the
// store's root plus the project-level ticket number. Used both to build the
// StageOp.Write relative path and the auto-commit caption. The supplied store
// must be the one that hosts the ticket (i.e. the one returned by
// hostStoreForTicket / ResolveProjectStore) — we explicitly avoid s.Store so
// per-repo mounts compute paths against the correct root.
func (s *Service) findTicketDirAndNumber(st *store.Store, slug, ticketID string) (string, int, error) {
	var rel string
	var number int
	err := st.WalkTickets(slug, func(ticketDir, _ string, tr *store.TicketRecord) error {
		if tr.ID != ticketID {
			return nil
		}
		// Convert absolute ticketDir back to a path relative to the store
		// root. Use filepath.Rel to be portable across separator quirks.
		r, relErr := filepath.Rel(st.Root, ticketDir)
		if relErr != nil {
			return relErr
		}
		rel = r
		number = tr.Number
		return nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("walk tickets: %w", err)
	}
	if rel == "" {
		return "", 0, fmt.Errorf("%w: ticket %q dir not found on disk", domain.ErrNotFound, ticketID)
	}
	return rel, number, nil
}

// hydrateAgentRef returns an AgentRef for the given agent id. If a name is
// already known (e.g. when the caller is the author and the requireSession
// middleware just resolved the agent), it's used directly. Otherwise we hit
// the store; not-found errors are swallowed and the ref is returned with just
// the id populated so the comment still surfaces an attribution token in the
// UI / MCP listing.
func hydrateAgentRef(as *store.AgentStore, id, fallbackName string) *domain.AgentRef {
	if id == "" {
		return nil
	}
	if fallbackName != "" {
		return &domain.AgentRef{ID: id, Name: fallbackName}
	}
	rec, err := as.ReadAgent(id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return &domain.AgentRef{ID: id}
		}
		// Any other read error: still return a thin ref — this is a read-side
		// hydration, not a critical path.
		return &domain.AgentRef{ID: id}
	}
	return &domain.AgentRef{ID: rec.ID, Name: rec.Name}
}

