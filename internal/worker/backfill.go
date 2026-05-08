package worker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"tickets_please/internal/store"
)

// Backfiller walks the on-disk tree and enqueues an embed Job for every
// source file lacking its sibling `*.embedding.json`. Used at boot to recover
// from `find -name '*.embedding.json' -delete`, crashes mid-job, or anything
// else that left sidecars stale.
type Backfiller struct {
	store  *store.Store
	worker *Worker
	log    *slog.Logger
}

// NewBackfiller wires a *store.Store + *Worker + logger into a Backfiller.
// A nil log defaults to slog.Default().
func NewBackfiller(st *store.Store, w *Worker, log *slog.Logger) *Backfiller {
	if log == nil {
		log = slog.Default()
	}
	return &Backfiller{store: st, worker: w, log: log}
}

// Run walks every project, phase, ticket, and comment looking for missing
// sidecars and enqueues a Job for each gap. Returns the first walk error if
// any; per-file IO problems for individual reads are logged and skipped (one
// unreadable summary shouldn't abort the whole backfill).
func (b *Backfiller) Run(ctx context.Context) error {
	if b == nil || b.store == nil || b.worker == nil {
		return nil
	}
	return b.store.WalkProjects(func(slug string, rec *store.ProjectRecord) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.backfillProjectSummary(ctx, slug, rec)
		b.backfillPhases(ctx, slug)
		b.backfillTickets(ctx, slug)
		return nil
	})
}

// enqueue routes through EnqueueBlocking so a slow worker back-pressures the
// walk instead of dropping jobs (default 256-slot queue overflowed and
// silently lost ~half the boot backfill on multi-project setups). Walk-level
// ctx-cancel surfaces back to Run via the per-callsite return.
func (b *Backfiller) enqueue(ctx context.Context, j Job) error {
	return b.worker.EnqueueBlocking(ctx, j)
}

// backfillProjectSummary checks projects/<slug>/summary.embedding.json and
// enqueues a JobProjectSummary if missing.
func (b *Backfiller) backfillProjectSummary(ctx context.Context, slug string, rec *store.ProjectRecord) {
	dir := b.store.ProjectDir(slug)
	src := filepath.Join(dir, "summary.md")
	side := filepath.Join(dir, "summary.embedding.json")
	if sidecarExists(side) {
		return
	}
	text, err := os.ReadFile(src)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			b.log.Warn("backfill: read project summary failed", "slug", slug, "err", err)
		}
		return
	}
	if err := b.enqueue(ctx, Job{
		Kind:        JobProjectSummary,
		SourcePath:  src,
		SidecarPath: side,
		EntryID:     rec.ID,
		Owner:       slug,
		Text:        string(text),
	}); err != nil {
		b.log.Debug("backfill: enqueue canceled", "slug", slug, "err", err)
	}
}

// backfillPhases walks every phase under the project and enqueues missing
// summary embeddings (phase summaries share the resident summaries index).
func (b *Backfiller) backfillPhases(ctx context.Context, slug string) {
	err := b.store.WalkPhases(slug, func(rec *store.PhaseRecord) error {
		dirName := fmt.Sprintf("%03d-%s", rec.Number, rec.Slug)
		phaseDir := b.store.PhaseDir(slug, dirName)
		src := filepath.Join(phaseDir, "summary.md")
		side := filepath.Join(phaseDir, "summary.embedding.json")
		if sidecarExists(side) {
			return nil
		}
		text, err := os.ReadFile(src)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				b.log.Warn("backfill: read phase summary failed", "slug", slug, "phase", rec.Slug, "err", err)
			}
			return nil
		}
		if err := b.enqueue(ctx, Job{
			Kind:        JobProjectSummary,
			SourcePath:  src,
			SidecarPath: side,
			EntryID:     rec.ID,
			Owner:       slug,
			Text:        string(text),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		b.log.Warn("backfill: walk phases failed", "slug", slug, "err", err)
	}
}

// backfillTickets walks every ticket (phased + phase-less) and enqueues
// missing body, learnings, and per-comment sidecars.
func (b *Backfiller) backfillTickets(ctx context.Context, slug string) {
	err := b.store.WalkTickets(slug, func(ticketDir, _ string, rec *store.TicketRecord) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.backfillTicketBody(ctx, ticketDir, slug, rec)
		b.backfillTicketLearnings(ctx, ticketDir, slug, rec)
		b.backfillTicketComments(ctx, ticketDir, slug, rec)
		return nil
	})
	if err != nil {
		b.log.Warn("backfill: walk tickets failed", "slug", slug, "err", err)
	}
}

// backfillTicketBody enqueues a JobTicketBody if body.embedding.json is
// missing. Text = title + "\n\n" + body, matching what the create/update
// handlers enqueue.
func (b *Backfiller) backfillTicketBody(ctx context.Context, ticketDir, slug string, rec *store.TicketRecord) {
	src := filepath.Join(ticketDir, "body.md")
	side := filepath.Join(ticketDir, "body.embedding.json")
	if sidecarExists(side) {
		return
	}
	body, err := os.ReadFile(src)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			b.log.Warn("backfill: read ticket body failed", "ticket_id", rec.ID, "err", err)
		}
		return
	}
	text := rec.Title + "\n\n" + string(body)
	if err := b.enqueue(ctx, Job{
		Kind:        JobTicketBody,
		SourcePath:  src,
		SidecarPath: side,
		EntryID:     rec.ID,
		Owner:       slug,
		Text:        text,
	}); err != nil {
		b.log.Debug("backfill: enqueue canceled", "ticket_id", rec.ID, "err", err)
	}
}

// backfillTicketLearnings enqueues a JobTicketLearnings if completion.md
// exists with a Learnings section but no learnings.embedding.json sibling.
func (b *Backfiller) backfillTicketLearnings(ctx context.Context, ticketDir, slug string, rec *store.TicketRecord) {
	src := filepath.Join(ticketDir, "completion.md")
	side := filepath.Join(ticketDir, "learnings.embedding.json")
	if sidecarExists(side) {
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		// completion.md only exists for done tickets — soft-skip.
		return
	}
	learnings := extractLearnings(string(data))
	if learnings == "" {
		return
	}
	if err := b.enqueue(ctx, Job{
		Kind:        JobTicketLearnings,
		SourcePath:  src,
		SidecarPath: side,
		EntryID:     rec.ID,
		Owner:       slug,
		Text:        learnings,
	}); err != nil {
		b.log.Debug("backfill: enqueue canceled", "ticket_id", rec.ID, "err", err)
	}
}

// backfillTicketComments walks comments/ and enqueues a JobComment for any
// .md file lacking its sibling `<stem>.embedding.json`.
func (b *Backfiller) backfillTicketComments(ctx context.Context, ticketDir, slug string, _ *store.TicketRecord) {
	err := b.store.WalkComments(ticketDir, func(rec *store.CommentRecord, body string) error {
		// Reconstruct the file path the same way the comment writer did. The
		// store's WalkComments doesn't expose the filename directly, but the
		// convention `<ts>-<short-id>-<kind>.md` plus the rec gives us enough
		// — we instead glob the comments dir for the matching id once per
		// ticket. Cheaper: list the dir and try matching by id prefix.
		commentsDir := filepath.Join(ticketDir, "comments")
		filename, ok := findCommentFilename(commentsDir, rec.ID)
		if !ok {
			return nil
		}
		src := filepath.Join(commentsDir, filename)
		stem := strings.TrimSuffix(filename, ".md")
		side := filepath.Join(commentsDir, stem+".embedding.json")
		if sidecarExists(side) {
			return nil
		}
		if err := b.enqueue(ctx, Job{
			Kind:        JobComment,
			SourcePath:  src,
			SidecarPath: side,
			EntryID:     rec.ID,
			Owner:       slug,
			Text:        body,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		b.log.Warn("backfill: walk comments failed", "ticket_dir", ticketDir, "err", err)
	}
}

// findCommentFilename returns the on-disk filename for a comment with the
// given UUID id. Comment filenames follow `<ts>-<short-id>-<kind>.md` where
// short-id is the first 4 bytes of the uuid hex-encoded — so we match by
// substring.
func findCommentFilename(dir, id string) (string, bool) {
	if len(id) < 8 {
		return "", false
	}
	// short-id is first 4 raw bytes of the uuid → 8 hex chars; uuid string
	// form starts with those 8 hex chars in lowercase before the first dash.
	short := strings.ToLower(strings.SplitN(id, "-", 2)[0])
	if len(short) > 8 {
		short = short[:8]
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		if strings.Contains(name, "-"+short+"-") {
			return name, true
		}
	}
	return "", false
}

// sidecarExists reports whether path is a regular file we can stat.
func sidecarExists(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !st.IsDir()
}

// extractLearnings returns the body of the `## Learnings` section of a
// completion.md, trimmed. Mirrors the cache loader's section parser but only
// pulls the one section we need. Returns "" if the section is absent.
func extractLearnings(md string) string {
	if md == "" {
		return ""
	}
	lines := strings.Split(md, "\n")
	in := false
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			heading := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			if heading == "learnings" || heading == "learning" {
				in = true
				continue
			}
			if in {
				break
			}
			continue
		}
		if in {
			out = append(out, line)
		}
	}
	return strings.Trim(strings.Join(out, "\n"), "\n \t")
}
