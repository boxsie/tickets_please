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
// during mount. Dimension-mismatched sidecars are silently skipped — the
// safeguard documented at the index Search layer (entries with the wrong
// length are ignored at query time) does the rest.

import (
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

// hydrateMount populates the four resident indexes from a freshly-mounted
// project's on-disk sidecars and enqueues missing ones onto the embed worker.
// All entries are tagged with the project slug as Owner.
//
// Errors from any one source file are warn-logged and skipped — a partial
// hydrate is strictly better than aborting the whole mount.
func (s *Service) hydrateMount(slug string, st *store.Store) {
	if st == nil {
		return
	}
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}

	if err := st.WalkProjects(func(_ string, rec *store.ProjectRecord) error {
		s.hydrateProjectSummary(slug, st, rec, log)
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("hydrate: walk projects failed", "slug", slug, "err", err)
	}

	if err := st.WalkPhases(slug, func(rec *store.PhaseRecord) error {
		s.hydratePhaseSummary(slug, st, rec, log)
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("hydrate: walk phases failed", "slug", slug, "err", err)
	}

	if err := st.WalkTickets(slug, func(ticketDir, _ string, rec *store.TicketRecord) error {
		s.hydrateTicketBody(slug, ticketDir, rec, log)
		s.hydrateTicketLearnings(slug, ticketDir, rec, log)
		s.hydrateTicketComments(slug, st, ticketDir, rec, log)
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("hydrate: walk tickets failed", "slug", slug, "err", err)
	}
}

// hydrateProjectSummary loads the project summary sidecar (or enqueues it).
func (s *Service) hydrateProjectSummary(slug string, st *store.Store, rec *store.ProjectRecord, log *slog.Logger) {
	dir := st.ProjectDir(slug)
	src := filepath.Join(dir, "summary.md")
	side := filepath.Join(dir, "summary.embedding.json")
	s.upsertOrEnqueue(slug, worker.JobProjectSummary, s.SummaryIdx, vecindex.KindProjectSummary,
		rec.ID, src, side, func() (string, error) {
			data, err := os.ReadFile(src)
			if err != nil {
				return "", err
			}
			return string(data), nil
		}, log)
}

// hydratePhaseSummary loads a phase summary sidecar (or enqueues it). Phase
// summaries share the resident SummaryIdx with project summaries — search
// methods filter by id-shape (only project ids count as project hits).
func (s *Service) hydratePhaseSummary(slug string, st *store.Store, rec *store.PhaseRecord, log *slog.Logger) {
	dirName := fmt.Sprintf("%03d-%s", rec.Number, rec.Slug)
	dir := st.PhaseDir(slug, dirName)
	src := filepath.Join(dir, "summary.md")
	side := filepath.Join(dir, "summary.embedding.json")
	s.upsertOrEnqueue(slug, worker.JobProjectSummary, s.SummaryIdx, vecindex.KindProjectSummary,
		rec.ID, src, side, func() (string, error) {
			data, err := os.ReadFile(src)
			if err != nil {
				return "", err
			}
			return string(data), nil
		}, log)
}

// hydrateTicketBody loads the ticket body sidecar (or enqueues it).
func (s *Service) hydrateTicketBody(slug, ticketDir string, rec *store.TicketRecord, log *slog.Logger) {
	src := filepath.Join(ticketDir, "body.md")
	side := filepath.Join(ticketDir, "body.embedding.json")
	s.upsertOrEnqueue(slug, worker.JobTicketBody, s.TicketsIdx, vecindex.KindTicketBody,
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
func (s *Service) hydrateTicketLearnings(slug, ticketDir string, rec *store.TicketRecord, log *slog.Logger) {
	src := filepath.Join(ticketDir, "completion.md")
	side := filepath.Join(ticketDir, "learnings.embedding.json")
	s.upsertOrEnqueue(slug, worker.JobTicketLearnings, s.LearningsIdx, vecindex.KindTicketLearnings,
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
func (s *Service) hydrateTicketComments(slug string, st *store.Store, ticketDir string, _ *store.TicketRecord, log *slog.Logger) {
	commentsDir := filepath.Join(ticketDir, "comments")
	if err := st.WalkComments(ticketDir, func(rec *store.CommentRecord, body string) error {
		filename, ok := findCommentFilenameByID(commentsDir, rec.ID)
		if !ok {
			return nil
		}
		src := filepath.Join(commentsDir, filename)
		stem := strings.TrimSuffix(filename, ".md")
		side := filepath.Join(commentsDir, stem+".embedding.json")
		s.upsertOrEnqueue(slug, worker.JobComment, s.CommentsIdx, vecindex.KindComment,
			rec.ID, src, side, func() (string, error) {
				return body, nil
			}, log)
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("hydrate: walk comments failed", "slug", slug, "ticket_dir", ticketDir, "err", err)
	}
}

// upsertOrEnqueue is the per-entry hot path: if the sidecar exists and parses
// as a vector, Upsert it directly into the resident index. Otherwise read the
// source text via getText and Enqueue an embed Job. Empty sidecars / empty
// text / missing source files are silently skipped.
func (s *Service) upsertOrEnqueue(
	slug string,
	jobKind worker.JobKind,
	idx *vecindex.Index,
	vKind vecindex.Kind,
	entryID, srcPath, sidePath string,
	getText func() (string, error),
	log *slog.Logger,
) {
	if vec, err := vecindex.ReadSidecar(sidePath); err == nil {
		// Don't add zero-length or wrong-dim vectors; index.Search would skip
		// them anyway, but keeping the entries map clean makes Snapshot
		// smaller and eviction by-owner cheaper.
		if len(vec) == s.EmbedDim {
			idx.Upsert(vecindex.Entry{
				ID:    entryID,
				Kind:  vKind,
				Owner: slug,
				Vec:   vec,
			})
			return
		}
		log.Debug("hydrate: sidecar dim mismatch, dropping",
			"slug", slug, "path", sidePath, "got", len(vec), "want", s.EmbedDim)
		return
	} else if !errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
		log.Warn("hydrate: read sidecar failed", "slug", slug, "path", sidePath, "err", err)
		return
	}

	// No sidecar — pull source text and hand it to the embed worker.
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
	if s.Worker == nil {
		return
	}
	s.Worker.Enqueue(worker.Job{
		Kind:        jobKind,
		SourcePath:  srcPath,
		SidecarPath: sidePath,
		EntryID:     entryID,
		Owner:       slug,
		Text:        text,
	})
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
// given slug. Called from the registry's eviction + (future) explicit
// unmount paths so search results stop including a project we can no longer
// load on disk.
func (s *Service) dropMountFromIndexes(slug string) {
	if slug == "" {
		return
	}
	if s.SummaryIdx != nil {
		s.SummaryIdx.RemoveByOwner(slug)
	}
	if s.TicketsIdx != nil {
		s.TicketsIdx.RemoveByOwner(slug)
	}
	if s.LearningsIdx != nil {
		s.LearningsIdx.RemoveByOwner(slug)
	}
	if s.CommentsIdx != nil {
		s.CommentsIdx.RemoveByOwner(slug)
	}
}
