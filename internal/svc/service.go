// Package svc implements the in-process Service API surface that the MCP
// transport (T12) and any future gRPC/HTTP transport call into. T15 owns the
// canonical Service struct + constructor; later tickets append their own
// fields and constructor wiring without replacing the type.
package svc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"tickets_please/internal/cache"
	"tickets_please/internal/config"
	"tickets_please/internal/embed"
	"tickets_please/internal/store"
	"tickets_please/internal/vecindex"
	"tickets_please/internal/worker"
)

// expectedEmbedDim is the on-disk embedding format's fixed dimensionality.
// Both the JSON sidecars and the in-memory vec index assume 768 floats per
// vector; switching providers with a different Dim() requires deleting all
// `*.embedding.json` files first. SPEC §Embedding pipeline pins this.
const expectedEmbedDim = 768

// Service is the in-process API surface. T15 declared the foundational
// fields; T10 appends the embedding provider, async worker, and four
// resident vec indexes. T11 will refactor TicketsIdx / CommentsIdx to
// per-project routing later.
type Service struct {
	Store      *store.Store
	AgentStore *store.AgentStore
	Logger     *slog.Logger
	Cfg        config.Config

	// Cache is the in-memory project cache (T04). Lazy-loads project trees
	// off disk, sliding-TTL evicts, and listens for cross-process file
	// changes via fsnotify.
	Cache *cache.ProjectCache

	// Embed is the embedding Provider used by Worker. Built from
	// cfg.EmbedProvider in New; the dim check happens before Worker starts
	// so a wrong provider fails loud.
	Embed embed.Provider

	// Worker is the async embedding goroutine. Handlers Enqueue jobs after
	// their StageOp commits; the worker drains the queue, writes the JSON
	// sidecar, and Upserts into the right resident index.
	Worker *worker.Worker

	// SummaryIdx holds project + phase summary embeddings. Resident.
	SummaryIdx *vecindex.Index
	// TicketsIdx holds ticket body embeddings. Resident; T11 may refactor
	// to per-project routing later.
	TicketsIdx *vecindex.Index
	// LearningsIdx holds completed-ticket learnings embeddings. Resident.
	LearningsIdx *vecindex.Index
	// CommentsIdx holds comment embeddings (user + system). Resident; T11
	// may refactor to per-project routing later.
	CommentsIdx *vecindex.Index

	// cacheCancel stops the background eviction goroutine. Held so tests
	// (and future graceful-shutdown paths) can tear it down.
	cacheCancel context.CancelFunc

	// cancelWorker stops the embedding worker (and the boot backfill
	// goroutine). Held so Close can drain in-flight jobs cleanly.
	cancelWorker context.CancelFunc

	// Agent debounce state. touchOnce tracks the last time we rewrote
	// LastSeenAt for a given agent id; touchMu guards the map.
	touchOnce map[string]time.Time
	touchMu   sync.Mutex
}

// New builds a Service: resolves the data dir into a *store.Store, wires a
// JSON-handler slog logger pointed at stderr, builds the project cache, the
// embedding provider, the four resident vec indexes, and the async embed
// worker. The dim check happens BEFORE the worker starts so a misconfigured
// provider fails loud rather than silently writing mismatched sidecars.
//
// The boot backfill walk runs in its own goroutine — startup never blocks on
// embedding latency.
func New(cfg config.Config) (*Service, error) {
	provider, err := embed.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("svc: build embed provider: %w", err)
	}
	return NewWithEmbed(cfg, provider)
}

// NewWithEmbed is the same as New but lets the caller inject an
// embed.Provider. Tests use this to drop in a deterministic fake (sha256 of
// text → 768 floats) without contacting a real Ollama / OpenAI server.
//
// The dim check still runs — a fake provider that returns the wrong shape is
// still a programming error.
func NewWithEmbed(cfg config.Config, provider embed.Provider) (*Service, error) {
	if provider == nil {
		return nil, fmt.Errorf("svc: nil embed provider")
	}
	if d := provider.Dim(); d != expectedEmbedDim {
		return nil, fmt.Errorf(
			"svc: embed provider %q returns %d-dim vectors but tickets_please pins %d on disk; "+
				"delete all *.embedding.json sidecars and reconsider the provider before retrying",
			provider.Name(), d, expectedEmbedDim,
		)
	}

	// Resolve the central data root. When DataRoot is empty (e.g. in tests that
	// supply only DataDir) fall back to a sibling tempdir-like path so tests
	// never pollute the user's real ~/.tickets_please.
	dataRoot := cfg.DataRoot
	if dataRoot == "" {
		dataRoot = cfg.DataDir + "-central"
	}
	as, err := store.NewAgentStore(dataRoot, cfg.LockTimeoutSeconds)
	if err != nil {
		return nil, fmt.Errorf("svc: build agent store: %w", err)
	}

	st, err := store.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("svc: build store: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	pc := cache.New(st, as, cfg)
	pc.Logger = logger

	indexes := worker.Indexes{
		Summaries: vecindex.New(),
		Tickets:   vecindex.New(),
		Learnings: vecindex.New(),
		Comments:  vecindex.New(),
	}
	w := worker.New(provider, indexes, 256, logger)

	evictCtx, cancelCache := context.WithCancel(context.Background())
	workerCtx, cancelWorker := context.WithCancel(context.Background())

	s := &Service{
		Store:        st,
		AgentStore:   as,
		Logger:       logger,
		Cfg:          cfg,
		Cache:        pc,
		Embed:        provider,
		Worker:       w,
		SummaryIdx:   indexes.Summaries,
		TicketsIdx:   indexes.Tickets,
		LearningsIdx: indexes.Learnings,
		CommentsIdx:  indexes.Comments,
		cacheCancel:  cancelCache,
		cancelWorker: cancelWorker,
		touchOnce:    make(map[string]time.Time),
	}
	go s.Cache.RunEvictor(evictCtx)
	go s.Worker.Run(workerCtx)

	// Boot backfill: enqueue any source files lacking sidecars. Runs async
	// so an empty / freshly-cloned data dir doesn't pay the cost on every
	// startup, and so a slow Ollama doesn't block service readiness.
	bf := worker.NewBackfiller(st, w, logger)
	go func() {
		if err := bf.Run(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("embed backfill failed", "err", err)
		}
	}()

	return s, nil
}

// Close stops background goroutines (cache evictor + embed worker) and
// releases all watcher resources. It blocks until the worker has drained
// any in-flight jobs so a caller can safely tear down the data dir afterward
// without racing the worker's sidecar writes.
//
// Safe to call multiple times.
func (s *Service) Close() {
	if s.cancelWorker != nil {
		s.cancelWorker()
		s.cancelWorker = nil
		if s.Worker != nil {
			s.Worker.Wait()
		}
	}
	if s.cacheCancel != nil {
		s.cacheCancel()
		s.cacheCancel = nil
	}
	if s.Cache != nil {
		s.Cache.CloseAll()
	}
}
