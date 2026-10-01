package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"tickets_please/internal/domain"
)

// Warning is a non-fatal integrity finding. The startup integrity check logs
// these and continues. Examples: orphan embedding sidecar, dangling agent
// reference, residual `.staging/<op-id>/`.
type Warning struct {
	Path    string
	Message string
}

func (w Warning) String() string {
	if w.Path == "" {
		return w.Message
	}
	return fmt.Sprintf("%s: %s", w.Path, w.Message)
}

// FatalError is a structural integrity failure. Examples: unparseable yaml,
// missing required file, a `done` ticket without `completion.md`. The
// startup integrity check aborts on any of these.
type FatalError struct {
	Path    string
	Message string
}

func (f FatalError) String() string {
	if f.Path == "" {
		return f.Message
	}
	return fmt.Sprintf("%s: %s", f.Path, f.Message)
}

func (f FatalError) Error() string { return f.String() }

// Integrity walks the data tree and returns the lists of warnings and fatal
// errors. The error return is reserved for an unexpected I/O failure during
// the walk itself; integrity findings live in the two slices.
//
// What it checks (per SPEC §Integrity check):
//   - Every `*.yaml` parses.
//   - Every project has `project.yaml` and `summary.md`.
//   - Every ticket has `ticket.yaml` and `body.md`.
//   - Every `done` ticket also has `completion.md`.
//   - Agent attribution refs (created_by, completed_by, author_id) resolve
//     to an existing agent file (warning if not) — only when agentSt != nil.
//   - `.staging/` is empty (warning per residual op-id; we don't auto-clean).
//   - Stray `*.embedding.json` without source file → warning.
//   - `*.embedding.failed` marker (the embed worker gave up on an entry, so
//     it is absent from search) → warning carrying the recorded cause.
//   - The same ticket id at more than one path → fatal.
//
// Deliberately NOT checked: a merely-absent `*.embedding.json`. Sidecars are
// gitignored and rebuilt by backfill, so on a fresh clone every one of them is
// missing; warning per entry would drown the findings that mean something.
// Absence means "not embedded yet", the marker means "tried and failed".
//
// agentSt may be nil; when it is, agent-ref validation is skipped (no warnings
// emitted for dangling refs). The next phase will always pass an AgentStore.
func (s *Store) Integrity(ctx context.Context, agentSt ...*AgentStore) ([]Warning, []FatalError, error) {
	var warnings []Warning
	var fatal []FatalError

	// Build the set of known agent ids so we can validate references.
	// Skip when no AgentStore is provided (backward compat for tests that
	// build a Store without the central data root).
	knownAgents := map[string]bool{}
	var as *AgentStore
	if len(agentSt) > 0 {
		as = agentSt[0]
	}
	if as != nil {
		if err := as.WalkAllAgents(func(rec *AgentRecord) error {
			knownAgents[rec.ID] = true
			return nil
		}); err != nil {
			return nil, nil, fmt.Errorf("walk agents: %w", err)
		}
	}

	// Residual .staging entries.
	stagingEntries, err := os.ReadDir(s.stagingDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("read staging dir: %w", err)
	}
	for _, e := range stagingEntries {
		warnings = append(warnings, Warning{
			Path:    filepath.Join(dirStaging, e.Name()),
			Message: "residual staging op (operation didn't finish; inspect contents)",
		})
	}

	// Walk the single project hosted at the data-dir root, if any. Post-
	// flatten there's at most one project per Store.
	checkAgents := as != nil // only validate agent refs when an AgentStore is available
	if err := s.WalkProjects(func(slug string, _ *ProjectRecord) error {
		w, f := s.checkProject(slug, knownAgents, checkAgents)
		warnings = append(warnings, w...)
		fatal = append(fatal, f...)
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("walk project: %w", err)
	}

	_ = ctx
	return warnings, fatal, nil
}

// checkProject runs the per-project structural checks.
// checkAgents gates agent-ref validation; it is false when no AgentStore was
// provided to Integrity (backward-compat path for tests that build a Store
// without a central data root).
func (s *Store) checkProject(slug string, knownAgents map[string]bool, checkAgents bool) ([]Warning, []FatalError) {
	var warnings []Warning
	var fatal []FatalError
	dir := s.projectDir(slug)

	// project.yaml required.
	rec := &ProjectRecord{}
	projectPath := filepath.Join(dir, fileProject)
	if err := ReadYAML(projectPath, rec); err != nil {
		fatal = append(fatal, FatalError{Path: projectPath, Message: err.Error()})
		return warnings, fatal
	}
	// summary.md required.
	summaryPath := filepath.Join(dir, fileSummary)
	if _, err := os.Stat(summaryPath); err != nil {
		fatal = append(fatal, FatalError{Path: summaryPath, Message: "missing required summary.md"})
	}
	if checkAgents && rec.CreatedByAgentID != nil && *rec.CreatedByAgentID != "" && !knownAgents[*rec.CreatedByAgentID] {
		warnings = append(warnings, Warning{Path: projectPath, Message: "created_by references unknown agent " + *rec.CreatedByAgentID})
	}
	// Stray summary embedding sidecar without source already covered (we
	// require summary.md). Walk for *.embedding.json without a source under
	// the project tree.
	warnings = append(warnings, scanOrphanEmbeddings(dir)...)
	warnings = append(warnings, scanEmbedFailures(dir)...)

	// Tickets — both phase-less and phased. seenTicketDirs catches the same
	// ticket id living at two paths, which a half-staged move can produce:
	// the store's behaviour with a duplicate id is undefined (which copy wins
	// on load, what the next number allocation sees), so it's structural.
	seenTicketDirs := map[string][]string{}
	if err := s.WalkTickets(slug, func(ticketDir, _ string, t *TicketRecord) error {
		if t.ID != "" {
			seenTicketDirs[t.ID] = append(seenTicketDirs[t.ID], ticketDir)
		}
		// body.md required.
		bodyPath := filepath.Join(ticketDir, fileBody)
		if _, err := os.Stat(bodyPath); err != nil {
			fatal = append(fatal, FatalError{Path: bodyPath, Message: "missing required body.md"})
		}
		if t.Column == domain.ColumnDone {
			cp := filepath.Join(ticketDir, fileCompletion)
			if _, err := os.Stat(cp); err != nil {
				fatal = append(fatal, FatalError{Path: cp, Message: "done ticket missing completion.md"})
			}
		}
		if checkAgents {
			if t.CreatedByAgentID != nil && *t.CreatedByAgentID != "" && !knownAgents[*t.CreatedByAgentID] {
				warnings = append(warnings, Warning{Path: filepath.Join(ticketDir, fileTicket), Message: "created_by references unknown agent " + *t.CreatedByAgentID})
			}
			if t.CompletedByAgentID != nil && *t.CompletedByAgentID != "" && !knownAgents[*t.CompletedByAgentID] {
				warnings = append(warnings, Warning{Path: filepath.Join(ticketDir, fileTicket), Message: "completed_by references unknown agent " + *t.CompletedByAgentID})
			}
		}
		// Comment author refs.
		if err := s.WalkComments(ticketDir, func(c *CommentRecord, _ string) error {
			if checkAgents && c.AuthorAgentID != nil && *c.AuthorAgentID != "" && !knownAgents[*c.AuthorAgentID] {
				warnings = append(warnings, Warning{
					Path:    filepath.Join(ticketDir, dirComments),
					Message: "comment " + c.ID + " author_id references unknown agent " + *c.AuthorAgentID,
				})
			}
			return nil
		}); err != nil {
			fatal = append(fatal, FatalError{Path: filepath.Join(ticketDir, dirComments), Message: err.Error()})
		}
		return nil
	}); err != nil {
		fatal = append(fatal, FatalError{Path: dir, Message: err.Error()})
	}
	for id, dirs := range seenTicketDirs {
		if len(dirs) < 2 {
			continue
		}
		sort.Strings(dirs)
		fatal = append(fatal, FatalError{
			Path:    dirs[0],
			Message: fmt.Sprintf("ticket %s exists at %d paths: %s", id, len(dirs), strings.Join(dirs, ", ")),
		})
	}
	return warnings, fatal
}

// scanEmbedFailures surfaces the `.embedding.failed` markers the embed worker
// leaves when it gives up on an entry.
//
// This is the answer to "is anything silently missing from search?". Note it
// deliberately does NOT warn about merely-absent sidecars: those are
// gitignored and regenerable, so on a fresh clone every single one is missing
// and a per-entry warning would bury the real signal in hundreds of lines.
// Absence means "not embedded yet"; a marker means "tried and failed", and
// only the second is news.
func scanEmbedFailures(root string) []Warning {
	var warnings []Warning
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".embedding.failed") {
			return nil
		}
		msg := "entry failed to embed and is absent from search"
		if data, readErr := os.ReadFile(path); readErr == nil {
			var rec struct {
				Error    string `json:"error"`
				Kind     string `json:"kind"`
				FailedAt string `json:"failed_at"`
			}
			if json.Unmarshal(data, &rec) == nil && rec.Error != "" {
				msg = fmt.Sprintf("%s embedding failed at %s and is absent from search: %s",
					rec.Kind, rec.FailedAt, rec.Error)
			}
		}
		warnings = append(warnings, Warning{Path: path, Message: msg})
		return nil
	})
	return warnings
}

// scanOrphanEmbeddings walks the project tree looking for `*.embedding.json`
// files whose source `<stem>.<ext>` is missing. Returns warnings only.
func scanOrphanEmbeddings(root string) []Warning {
	var warnings []Warning
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".embedding.json") {
			return nil
		}
		stem := strings.TrimSuffix(name, ".embedding.json")
		dir := filepath.Dir(path)
		// Most sidecars share their stem with their source (`summary.md`,
		// `body.md`, a comment file). Learnings are the exception: the text
		// lives in a *section* of `completion.md`, so `learnings.embedding
		// .json` has no `learnings.md` beside it and a stem-only rule
		// mislabels every completed ticket as an orphan.
		candidates := []string{stem + ".md", stem + ".yaml"}
		if stem == "learnings" {
			candidates = append(candidates, fileCompletion)
		}
		found := false
		for _, c := range candidates {
			if _, err := os.Stat(filepath.Join(dir, c)); err == nil {
				found = true
				break
			}
		}
		if !found {
			warnings = append(warnings, Warning{Path: path, Message: "orphan embedding sidecar (no source file)"})
		}
		return nil
	})
	return warnings
}
