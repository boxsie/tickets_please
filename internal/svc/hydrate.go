package svc

// Resident-index hydration for newly-mounted projects (T008).
//
// On RegisterProjectMount, walk the project's on-disk tree and populate the
// four resident indexes (SummaryIdx / TicketsIdx / LearningsIdx / CommentsIdx)
// with everything that already has an `*.embedding.json` sidecar — tagged
// with the project's slug as the Owner so search results carry provenance and
// eviction can drop them by slug.
//
// Sidecars that are missing get enqueued via the existing async embed worker
// so a freshly-cloned repo doesn't pay the embedding latency synchronously
// during mount. Sidecars whose recorded (Provider, Model) pair doesn't match
// the mount's expected pair — including the legacy flat-array shape that
// fails to decode entirely — are deleted and re-enqueued so a config change
// like flipping `embed_model: nomic-embed-text → bge-m3` triggers a clean
// rebuild on next start instead of silently mixing models in one index.

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
	"tickets_please/internal/vecindex"
	"tickets_please/internal/worker"
)

// hydrateMount populates the per-mount resident indexes from a freshly-
// mounted project's on-disk sidecars and enqueues missing ones onto the
// embed worker. All entries are tagged with the project slug as Owner.
//
// Errors from any one source file are warn-logged and skipped — a partial
// hydrate is strictly better than aborting the whole mount.
func (s *Service) hydrateMount(slug string, mount *ProjectMount) {
	if mount == nil || mount.Store == nil {
		return
	}
	st := mount.Store
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}

	if err := st.WalkProjects(func(_ string, rec *store.ProjectRecord) error {
		s.hydrateProjectSummary(slug, mount, rec, log)
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("hydrate: walk projects failed", "slug", slug, "err", err)
	}

	if err := st.WalkPhases(slug, func(rec *store.PhaseRecord) error {
		s.hydratePhaseSummary(slug, mount, rec, log)
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("hydrate: walk phases failed", "slug", slug, "err", err)
	}

	if err := st.WalkTickets(slug, func(ticketDir, _ string, rec *store.TicketRecord) error {
		s.hydrateTicketBody(slug, mount, ticketDir, rec, log)
		s.hydrateTicketLearnings(slug, mount, ticketDir, rec, log)
		s.hydrateTicketComments(slug, mount, ticketDir, rec, log)
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("hydrate: walk tickets failed", "slug", slug, "err", err)
	}
}

// hydrateProjectSummary loads the project summary sidecar (or enqueues it).
func (s *Service) hydrateProjectSummary(slug string, mount *ProjectMount, rec *store.ProjectRecord, log *slog.Logger) {
	dir := mount.Store.ProjectDir(slug)
	src := filepath.Join(dir, "summary.md")
	side := filepath.Join(dir, "summary.embedding.json")
	s.upsertOrEnqueue(slug, mount, worker.JobProjectSummary, mount.SummaryIdx, vecindex.KindProjectSummary,
		rec.ID, src, side, func() (string, error) {
			data, err := os.ReadFile(src)
			if err != nil {
				return "", err
			}
			return string(data), nil
		}, log)
}

// hydratePhaseSummary loads a phase summary sidecar (or enqueues it). Phase
// summaries share SummaryIdx with project summaries — search methods filter
// by id-shape (only project ids count as project hits).
func (s *Service) hydratePhaseSummary(slug string, mount *ProjectMount, rec *store.PhaseRecord, log *slog.Logger) {
	dirName := fmt.Sprintf("%03d-%s", rec.Number, rec.Slug)
	dir := mount.Store.PhaseDir(slug, dirName)
	src := filepath.Join(dir, "summary.md")
	side := filepath.Join(dir, "summary.embedding.json")
	s.upsertOrEnqueue(slug, mount, worker.JobProjectSummary, mount.SummaryIdx, vecindex.KindProjectSummary,
		rec.ID, src, side, func() (string, error) {
			data, err := os.ReadFile(src)
			if err != nil {
				return "", err
			}
			return string(data), nil
		}, log)
}

// hydrateTicketBody loads the ticket body sidecar (or enqueues it).
func (s *Service) hydrateTicketBody(slug string, mount *ProjectMount, ticketDir string, rec *store.TicketRecord, log *slog.Logger) {
	src := filepath.Join(ticketDir, "body.md")
	side := filepath.Join(ticketDir, "body.embedding.json")
	s.upsertOrEnqueue(slug, mount, worker.JobTicketBody, mount.TicketsIdx, vecindex.KindTicketBody,
		rec.ID, src, side, func() (string, error) {
			body, err := os.ReadFile(src)
			if err != nil {
				return "", err
			}
			return rec.Title + "\n\n" + string(body), nil
		}, log)
}

// hydrateTicketLearnings loads the learnings sidecar (or enqueues it). Only
// completed tickets have a completion.md; missing-source soft-skips.
func (s *Service) hydrateTicketLearnings(slug string, mount *ProjectMount, ticketDir string, rec *store.TicketRecord, log *slog.Logger) {
	src := filepath.Join(ticketDir, "completion.md")
	side := filepath.Join(ticketDir, "learnings.embedding.json")
	s.upsertOrEnqueue(slug, mount, worker.JobTicketLearnings, mount.LearningsIdx, vecindex.KindTicketLearnings,
		rec.ID, src, side, func() (string, error) {
			data, err := os.ReadFile(src)
			if err != nil {
				return "", err
			}
			learnings := extractLearningsSection(string(data))
			if learnings == "" {
				return "", fs.ErrNotExist
			}
			return learnings, nil
		}, log)
}

// hydrateTicketComments walks the ticket's comments dir and loads/enqueues
// each comment's sidecar.
func (s *Service) hydrateTicketComments(slug string, mount *ProjectMount, ticketDir string, _ *store.TicketRecord, log *slog.Logger) {
	commentsDir := filepath.Join(ticketDir, "comments")
	if err := mount.Store.WalkComments(ticketDir, func(rec *store.CommentRecord, body string) error {
		filename, ok := findCommentFilenameByID(commentsDir, rec.ID)
		if !ok {
			return nil
		}
		src := filepath.Join(commentsDir, filename)
		stem := strings.TrimSuffix(filename, ".md")
		side := filepath.Join(commentsDir, stem+".embedding.json")
		s.upsertOrEnqueue(slug, mount, worker.JobComment, mount.CommentsIdx, vecindex.KindComment,
			rec.ID, src, side, func() (string, error) {
				return body, nil
			}, log)
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("hydrate: walk comments failed", "slug", slug, "ticket_dir", ticketDir, "err", err)
	}
}

// upsertOrEnqueue is the per-entry hot path: if the sidecar exists, parses,
// and was stamped by the mount's current (Provider, Model) pair, Upsert it
// directly into the resident index. A stale stamp — or any decode error,
// e.g. a legacy flat-array sidecar — drops the file from disk and falls
// through to the missing-sidecar branch so the worker re-embeds it under
// the new identity. Missing source / empty text are silently skipped.
func (s *Service) upsertOrEnqueue(
	slug string,
	mount *ProjectMount,
	jobKind worker.JobKind,
	idx *vecindex.Index,
	vKind vecindex.Kind,
	entryID, srcPath, sidePath string,
	getText func() (string, error),
	log *slog.Logger,
) {
	sc, err := vecindex.ReadSidecar(sidePath)
	switch {
	case err == nil:
		if !staleSidecar(sc, mount) {
			if idx != nil {
				idx.Upsert(vecindex.Entry{
					ID:    entryID,
					Kind:  vKind,
					Owner: slug,
					Vec:   sc.Vec,
				})
			}
			return
		}
		log.Debug("sidecar provider/model mismatch, re-embedding",
			"slug", slug, "path", sidePath,
			"expected", expectedIdentity(mount),
			"got", fmt.Sprintf("%s/%s", sc.Provider, sc.Model))
		dropStaleSidecar(sidePath, slug, log)
		// fall through to the missing-sidecar enqueue branch below.
	case errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err):
		// No sidecar yet — cold-clone path. Fall through to enqueue.
	default:
		// Decode error (truncated JSON, legacy flat-array shape, perms, …).
		// Treat the same as a stale sidecar: warn, drop, re-enqueue.
		log.Warn("hydrate: read sidecar failed; treating as stale",
			"slug", slug, "path", sidePath, "err", err)
		dropStaleSidecar(sidePath, slug, log)
	}

	// No usable sidecar — pull source text and hand it to the embed worker.
	text, err := getText()
	if err != nil {
		// Source missing (deleted ticket, no completion.md, etc.) is normal;
		// only warn for unexpected IO errors.
		if !errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
			log.Warn("hydrate: read source failed", "slug", slug, "path", srcPath, "err", err)
		}
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	if mount == nil || mount.Worker == nil {
		return
	}
	// Boot-time / re-mount path: block until the worker accepts. Dropping
	// here means the source file goes un-embedded until the next restart's
	// staleness/backfill pass picks it up — slow + noisy. Backfilling 200
	// projects through a 256-slot queue would otherwise lose ~half the jobs
	// on a cold boot. context.Background() is fine: hydrate is bounded by
	// the on-disk tree and the worker drains continuously.
	if err := mount.Worker.EnqueueBlocking(context.Background(), worker.Job{
		Kind:        jobKind,
		SourcePath:  srcPath,
		SidecarPath: sidePath,
		EntryID:     entryID,
		Owner:       slug,
		Text:        text,
	}); err != nil {
		log.Warn("hydrate: enqueue canceled", "slug", slug, "path", srcPath, "err", err)
	}
}

// staleSidecar reports whether sc was stamped by a different (Provider, Model)
// pair than mount currently expects. A nil mount or one without an Embed
// provider can't compare, so it's treated as not-stale (lets the legacy
// fallback path keep working in tests that build mounts by hand).
func staleSidecar(sc vecindex.Sidecar, mount *ProjectMount) bool {
	if mount == nil || mount.Embed == nil {
		return false
	}
	return sc.Provider != mount.Embed.Name() || sc.Model != mount.EmbedModel
}

// expectedIdentity formats the mount's expected (Provider, Model) pair for
// log fields. Pulled out so the log call stays readable.
func expectedIdentity(mount *ProjectMount) string {
	if mount == nil || mount.Embed == nil {
		return "/"
	}
	return fmt.Sprintf("%s/%s", mount.Embed.Name(), mount.EmbedModel)
}

// dropStaleSidecar removes a sidecar that's been judged unusable. A failure
// to remove is warn-logged but doesn't propagate — the worker write that
// follows the re-enqueue uses an atomic rename, so a leftover file gets
// overwritten on the next pass anyway.
func dropStaleSidecar(sidePath, slug string, log *slog.Logger) {
	if err := os.Remove(sidePath); err != nil && !errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
		log.Warn("hydrate: remove stale sidecar failed",
			"slug", slug, "path", sidePath, "err", err)
	}
}

// extractLearningsSection mirrors worker.extractLearnings but lives here to
// avoid an internal cycle (worker doesn't export it).
func extractLearningsSection(md string) string {
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

// findCommentFilenameByID is the same convention worker.findCommentFilename
// uses but inlined to avoid exporting it from worker. Comment filenames are
// `<ts>-<short-id>-<kind>.md` where short-id is the first 8 hex chars of the
// uuid; we match by substring.
func findCommentFilenameByID(dir, id string) (string, bool) {
	if len(id) < 8 {
		return "", false
	}
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

// dropMountFromIndexes evicts every resident-index entry tagged with the
// given slug. Nils the per-mount indexes (the same shape LRU eviction uses)
// and walks the worker's defaultIndexes by-owner so the registry-empty
// stdio fallback path drops its entries too.
//
// Caller must hold mountsMu — every existing caller already does (the LRU
// eviction path inside maybeEvictLocked, and the dedicated test that
// manually mirrors it).
func (s *Service) dropMountFromIndexes(slug string) {
	if slug == "" {
		return
	}
	if mount, ok := s.projectMounts[slug]; ok && mount != nil {
		mount.SummaryIdx = nil
		mount.TicketsIdx = nil
		mount.LearningsIdx = nil
		mount.CommentsIdx = nil
	}
	if s.defaultIndexes.Summaries != nil {
		s.defaultIndexes.Summaries.RemoveByOwner(slug)
	}
	if s.defaultIndexes.Tickets != nil {
		s.defaultIndexes.Tickets.RemoveByOwner(slug)
	}
	if s.defaultIndexes.Learnings != nil {
		s.defaultIndexes.Learnings.RemoveByOwner(slug)
	}
	if s.defaultIndexes.Comments != nil {
		s.defaultIndexes.Comments.RemoveByOwner(slug)
	}
}
