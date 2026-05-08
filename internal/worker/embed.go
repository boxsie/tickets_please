// Package worker implements the async embedding pipeline. Service handlers
// fire-and-forget enqueue jobs after their store writes commit; this worker
// drains the channel, calls the embedding Provider, writes the
// `*.embedding.json` sidecar, and Upserts the resulting vector into the right
// in-memory vecindex.Index.
//
// The worker is intentionally lossy: a full buffer warn-logs and drops the
// job; an embed call that errors warn-logs and skips. Backfill picks anything
// up on the next project load (or boot), so dropped jobs are recoverable.
//
// Per-project vector index routing is T11's job. T10 writes everything into
// four resident global indexes; T11 may later refactor to attach per-project
// slices to LoadedProject.
package worker

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"tickets_please/internal/embed"
	"tickets_please/internal/vecindex"
)

// JobKind selects which target index the worker upserts into and which
// vecindex.Kind the resulting Entry is tagged with.
type JobKind int

const (
	JobUnspecified JobKind = iota
	JobProjectSummary
	JobTicketBody
	JobTicketLearnings
	JobComment
)

// Job is one unit of embedding work. The caller has already extracted Text
// (e.g. learnings section only, title+"\n\n"+body for tickets) so the worker
// is purely transport.
type Job struct {
	Kind        JobKind
	SourcePath  string // for backfill diagnostics
	SidecarPath string // <stem>.embedding.json next to the source file
	EntryID     string // project.id / ticket.id / comment.id
	Owner       string // project slug
	Text        string // already-extracted text

	// flushDone, when non-nil, is closed by the worker after this Job is
	// dequeued — used as a barrier by Flush so callers can wait for every
	// previously-enqueued job to finish before proceeding (e.g. a
	// destructive delete).
	flushDone chan struct{}
}

// Indexes carries the four resident global indexes the worker writes into.
// Phase summaries share Summaries with project summaries.
type Indexes struct {
	Summaries *vecindex.Index // project + phase summaries
	Tickets   *vecindex.Index // ticket bodies
	Learnings *vecindex.Index // ticket learnings
	Comments  *vecindex.Index // user + system comments
}

// IndexResolver maps a Job's (kind, owner-slug) to the *vecindex.Index its
// resulting Entry should be Upserted into. The Service uses this to route
// worker writes into per-project indexes (W2-T1: indexes live on
// ProjectMount, not on Service). Returning nil falls back to the worker's
// own static Indexes — that's the registry-empty stdio path.
type IndexResolver func(kind JobKind, owner string) *vecindex.Index

// Worker is the goroutine that drains a buffered channel of Jobs.
type Worker struct {
	queue    chan Job
	provider embed.Provider
	model    string // model identifier stamped into sidecars (e.g. "nomic-embed-text")
	indexes  Indexes
	resolve  IndexResolver
	log      *slog.Logger

	// wg tracks the Run goroutine so callers can Wait for graceful drain.
	// Add(1) happens once at Worker construction so Wait is safe to call
	// even before Run actually starts executing — it blocks until the
	// (eventual) Run goroutine returns.
	wg      sync.WaitGroup
	runOnce sync.Once
}

// New constructs a Worker with the given provider, target indexes, and queue
// buffer size. A nil log defaults to slog.Default() so callers don't have to
// special-case it. Wait blocks until Run returns.
//
// The embedder model identifier (stamped into every sidecar the worker
// writes) defaults to empty and can be set after construction with
// SetModel. The provider's Name() is read fresh on each write so it always
// reflects the current Provider.
func New(provider embed.Provider, indexes Indexes, bufferSize int, log *slog.Logger) *Worker {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	if log == nil {
		log = slog.Default()
	}
	w := &Worker{
		queue:    make(chan Job, bufferSize),
		provider: provider,
		indexes:  indexes,
		log:      log,
	}
	w.wg.Add(1)
	return w
}

// SetModel records the embedder model identifier (e.g. "nomic-embed-text",
// "bge-m3") that the Service-supplied provider was configured with. Stamped
// into every sidecar the worker writes from this point on, so a future
// hydrate can detect "wrong embedder" and re-enqueue.
//
// Safe to call before Run starts; not safe to race with concurrent process
// calls (set once at Service init, then leave alone).
func (w *Worker) SetModel(model string) {
	if w == nil {
		return
	}
	w.model = model
}

// SetIndexResolver installs a slug-aware index lookup so Worker.Upsert can
// land in per-mount indexes (the W2-T1 model). When the resolver returns
// nil, the worker falls back to its own static Indexes (the registry-empty
// stdio path). Safe to call before Run starts; not safe to race with
// concurrent process calls.
func (w *Worker) SetIndexResolver(r IndexResolver) {
	if w == nil {
		return
	}
	w.resolve = r
}

// Wait blocks until Run has returned. Used by Service.Close so the test
// teardown of a tempdir doesn't race a still-flushing worker.
func (w *Worker) Wait() {
	if w == nil {
		return
	}
	w.wg.Wait()
}

// Flush blocks until every job currently in the queue has been processed.
// Used by mutating handlers that follow up with a destructive store op
// (DeleteProject, DeletePhase) so the worker doesn't write a sidecar into a
// directory the same goroutine is about to remove.
//
// Jobs enqueued AFTER Flush returns are not waited on. Flush is a barrier,
// not a quiesce. Returns immediately if w is nil or if the worker is shut
// down (queue closed).
func (w *Worker) Flush(ctx context.Context) {
	if w == nil {
		return
	}
	// Sentinel signaled via a per-call channel pushed onto the queue. When
	// the worker drains it, it closes the channel — at that point every job
	// previously in the queue has already been processed (channels are
	// FIFO).
	done := make(chan struct{})
	select {
	case w.queue <- Job{Kind: JobUnspecified, flushDone: done}:
	case <-ctx.Done():
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Enqueue submits a job non-blockingly. On a full buffer the worker warn-logs
// and drops; backfill on next startup will recover the missing sidecar.
func (w *Worker) Enqueue(j Job) {
	if w == nil {
		return
	}
	select {
	case w.queue <- j:
	default:
		w.log.Warn("embed worker queue full; dropping job",
			"kind", j.Kind, "source", j.SourcePath, "entry_id", j.EntryID)
	}
}

// Run drains the queue until ctx is canceled. On cancellation it processes
// any in-flight jobs already in the channel (a best-effort drain) and then
// returns. Per-job errors are warn-logged and skipped — never fatal.
//
// During the post-cancel drain, jobs are processed against context.Background
// (with a short overall budget) so a real provider — which honors the job
// ctx — doesn't immediately error out on every drained job.
//
// Run must only be called once per Worker.
func (w *Worker) Run(ctx context.Context) {
	defer w.runOnce.Do(func() { w.wg.Done() })

	for {
		select {
		case <-ctx.Done():
			drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for {
				select {
				case j := <-w.queue:
					w.process(drainCtx, j)
				default:
					return
				}
			}
		case j := <-w.queue:
			w.process(ctx, j)
		}
	}
}

// process is the per-job loop body: embed → write sidecar → upsert.
func (w *Worker) process(ctx context.Context, j Job) {
	// Flush sentinel: signal and return. Sentinel jobs carry no text and
	// only exist to give Flush a FIFO barrier point.
	if j.flushDone != nil {
		close(j.flushDone)
		return
	}
	if j.Text == "" {
		// Empty text yields an empty/zero vector which is meaningless for
		// search — skip silently rather than persist a useless sidecar.
		w.log.Debug("embed worker: empty text; skipping",
			"kind", j.Kind, "source", j.SourcePath, "entry_id", j.EntryID)
		return
	}
	// Source-still-present check: the source file may have been deleted (or
	// its parent project/phase/ticket removed) between Enqueue and now. If
	// so, writing the sidecar would recreate the parent directory and
	// resurrect a doomed entry. Skip the write — the entity is gone, the
	// vec entry will go nowhere useful, and we'd rather lose the embed than
	// undo the delete.
	if j.SourcePath != "" {
		if _, err := os.Stat(j.SourcePath); err != nil {
			w.log.Debug("embed worker: source file missing; skipping",
				"kind", j.Kind, "source", j.SourcePath, "entry_id", j.EntryID, "err", err)
			return
		}
	}
	vec, err := w.provider.Embed(ctx, j.Text)
	if err != nil {
		w.log.Warn("embed failed",
			"err", err, "kind", j.Kind, "source", j.SourcePath, "entry_id", j.EntryID)
		return
	}
	if j.SidecarPath != "" {
		sc := vecindex.Sidecar{
			Provider: w.provider.Name(),
			Model:    w.model,
			Dim:      len(vec),
			Vec:      vec,
		}
		if err := vecindex.WriteSidecar(j.SidecarPath, sc); err != nil {
			w.log.Warn("write embedding sidecar failed",
				"err", err, "path", j.SidecarPath, "entry_id", j.EntryID)
			return
		}
	}
	var idx *vecindex.Index
	if w.resolve != nil {
		idx = w.resolve(j.Kind, j.Owner)
	}
	if idx == nil {
		idx = w.indexFor(j.Kind)
	}
	if idx == nil {
		w.log.Warn("no target index for job kind",
			"kind", j.Kind, "source", j.SourcePath, "entry_id", j.EntryID)
		return
	}
	idx.Upsert(vecindex.Entry{
		ID:    j.EntryID,
		Kind:  vecKindFor(j.Kind),
		Owner: j.Owner,
		Vec:   vec,
	})
}

// indexFor maps a JobKind to its target resident index. Returns nil for
// unrecognized kinds.
func (w *Worker) indexFor(k JobKind) *vecindex.Index {
	switch k {
	case JobProjectSummary:
		return w.indexes.Summaries
	case JobTicketBody:
		return w.indexes.Tickets
	case JobTicketLearnings:
		return w.indexes.Learnings
	case JobComment:
		return w.indexes.Comments
	default:
		return nil
	}
}

// vecKindFor maps a JobKind to the vecindex.Kind tag that goes on the Entry.
func vecKindFor(k JobKind) vecindex.Kind {
	switch k {
	case JobProjectSummary:
		return vecindex.KindProjectSummary
	case JobTicketBody:
		return vecindex.KindTicketBody
	case JobTicketLearnings:
		return vecindex.KindTicketLearnings
	case JobComment:
		return vecindex.KindComment
	default:
		return vecindex.KindUnspecified
	}
}
