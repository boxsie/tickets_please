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
// W2-T2 lands per-mount workers: one Worker (one goroutine, one buffered
// channel) per ProjectMount, owning the four indexes for that mount. The
// Service tears each one down on eviction.
package worker

import (
	"context"
	"fmt"
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

// Indexes carries the four target indexes the worker writes into. Phase
// summaries share Summaries with project summaries. Per-mount workers own
// one Indexes bundle each.
type Indexes struct {
	Summaries *vecindex.Index // project + phase summaries
	Tickets   *vecindex.Index // ticket bodies
	Learnings *vecindex.Index // ticket learnings
	Comments  *vecindex.Index // user + system comments
}

// Worker is the goroutine that drains a buffered channel of Jobs.
type Worker struct {
	queue    chan Job
	provider embed.Provider
	model    string // model identifier stamped into sidecars (e.g. "nomic-embed-text")
	indexes  Indexes
	log      *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup

	stopOnce sync.Once
}

// New constructs a Worker bound to provider+model+indexes, starts the worker
// goroutine, and returns. Stop() (or context-cancel via ctx) tears the
// goroutine down. A nil log defaults to slog.Default().
//
// The model identifier is stamped into every sidecar this worker writes, so
// hydrate can detect "wrong embedder" and re-enqueue (W2-T3).
func New(ctx context.Context, provider embed.Provider, model string, indexes Indexes, bufferSize int, log *slog.Logger) *Worker {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	if log == nil {
		log = slog.Default()
	}
	runCtx, cancel := context.WithCancel(ctx)
	w := &Worker{
		queue:    make(chan Job, bufferSize),
		provider: provider,
		model:    model,
		indexes:  indexes,
		log:      log,
		cancel:   cancel,
	}
	w.wg.Add(1)
	go w.run(runCtx)
	return w
}

// Flush blocks until every job currently in the queue has been processed.
// Used by mutating handlers that follow up with a destructive store op
// (DeleteProject, DeletePhase, DeleteTicket) so the worker doesn't write a
// sidecar into a directory the same goroutine is about to remove.
//
// Jobs enqueued AFTER Flush returns are not waited on. Flush is a barrier,
// not a quiesce. Returns immediately if w is nil or ctx is canceled.
func (w *Worker) Flush(ctx context.Context) {
	if w == nil {
		return
	}
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

// Stop drains the queue, terminates the goroutine, and waits for it to
// return. Safe to call multiple times. Pass a context with a deadline so a
// stuck provider can't pin the call indefinitely; on ctx-cancel the goroutine
// is force-stopped (in-flight jobs may be dropped). Returns when the run
// goroutine has returned or ctx fires.
func (w *Worker) Stop(ctx context.Context) {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		w.cancel()
	})
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Enqueue submits a job non-blockingly. On a full buffer the worker warn-logs
// and drops; backfill on next startup will recover the missing sidecar.
//
// Use this from live-request paths (handlers that have an HTTP caller to
// back-pressure to via 5xx). For boot-time backfill / hydrate where dropping
// means data loss until the next restart, use EnqueueBlocking.
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

// EnqueueBlocking submits a job, blocking until the worker accepts it or ctx
// cancels. Returns ctx.Err() on cancellation, nil on accept. No drop-on-full
// warning — the boot-time backfill / hydrate caller has nobody to
// back-pressure to and dropping = data loss until next restart.
func (w *Worker) EnqueueBlocking(ctx context.Context, j Job) error {
	if w == nil {
		return nil
	}
	select {
	case w.queue <- j:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run drains the queue until ctx is canceled. On cancellation it processes
// any in-flight jobs already in the channel (a best-effort drain) and then
// returns. Per-job errors are warn-logged and skipped — never fatal.
func (w *Worker) run(ctx context.Context) {
	defer w.wg.Done()

	for {
		select {
		case <-ctx.Done():
			drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			for {
				select {
				case j := <-w.queue:
					w.process(drainCtx, j)
				default:
					cancel()
					return
				}
			}
		case j := <-w.queue:
			w.process(ctx, j)
		}
	}
}

// process is the per-job loop body: embed → write sidecar → upsert. Learnings
// are paragraph-chunked and embedded as multiple vectors; other source kinds
// retain their single-vector behaviour.
func (w *Worker) process(ctx context.Context, j Job) {
	if j.flushDone != nil {
		close(j.flushDone)
		return
	}
	if j.Text == "" {
		w.log.Debug("embed worker: empty text; skipping",
			"kind", j.Kind, "source", j.SourcePath, "entry_id", j.EntryID)
		return
	}
	if j.SourcePath != "" {
		if _, err := os.Stat(j.SourcePath); err != nil {
			w.log.Debug("embed worker: source file missing; skipping",
				"kind", j.Kind, "source", j.SourcePath, "entry_id", j.EntryID, "err", err)
			return
		}
	}
	texts := []string{j.Text}
	if j.Kind == JobTicketLearnings {
		texts = chunkLearningText(j.Text)
	}
	if len(texts) == 0 {
		w.log.Debug("embed worker: no chunks; skipping",
			"kind", j.Kind, "source", j.SourcePath, "entry_id", j.EntryID)
		return
	}
	vecs := make([][]float32, 0, len(texts))
	var err error
	for i, text := range texts {
		chunkJob := j
		chunkJob.Text = text
		var vec []float32
		vec, err = w.embedWithRetry(ctx, chunkJob)
		// Belt and braces: the shipped providers validate their own output,
		// but this is the last gate before the vector reaches the index.
		if err == nil {
			err = embed.ValidateVector(w.provider.Name(), vec)
		}
		if err == nil && len(vecs) > 0 && len(vec) != len(vecs[0]) {
			err = fmt.Errorf("vector dim %d differs from first chunk dim %d", len(vec), len(vecs[0]))
		}
		if err != nil {
			err = fmt.Errorf("chunk %d/%d: %w", i+1, len(texts), err)
			break
		}
		vecs = append(vecs, vec)
	}
	if err != nil {
		// A log line is not a record. This job is fire-and-forget off an
		// already-successful mutation, so without a durable marker the entry
		// is simply absent from search forever and nothing anywhere says so.
		// The marker is what `tickets_please check` reads.
		w.log.Warn("embed failed",
			"err", err, "kind", j.Kind, "source", j.SourcePath, "entry_id", j.EntryID)
		w.writeFailureMarker(j, err)
		return
	}
	// A previous attempt may have left a marker; this one succeeded.
	w.clearFailureMarker(j)
	var vec []float32
	if len(vecs) > 0 {
		vec = vecs[0]
	}
	if j.SidecarPath != "" {
		sc := vecindex.Sidecar{
			Provider: w.provider.Name(),
			Model:    w.model,
			Dim:      len(vec),
		}
		if j.Kind == JobTicketLearnings {
			sc.Vecs = vecs
		} else {
			sc.Vec = vec
		}
		if err := vecindex.WriteSidecar(j.SidecarPath, sc); err != nil {
			w.log.Warn("write embedding sidecar failed",
				"err", err, "path", j.SidecarPath, "entry_id", j.EntryID)
			return
		}
	}
	idx := w.indexFor(j.Kind)
	if idx == nil {
		w.log.Warn("no target index for job kind",
			"kind", j.Kind, "source", j.SourcePath, "entry_id", j.EntryID)
		return
	}
	entry := vecindex.Entry{
		ID:    j.EntryID,
		Kind:  vecKindFor(j.Kind),
		Owner: j.Owner,
	}
	if j.Kind == JobTicketLearnings {
		entry.Vecs = vecs
	} else {
		entry.Vec = vec
	}
	idx.Upsert(entry)
	w.log.Info("embedded",
		"kind", j.Kind, "owner", j.Owner, "entry_id", j.EntryID,
		"dim", len(vec), "vectors", len(vecs))
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
