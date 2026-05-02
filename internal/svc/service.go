// Package svc implements the in-process Service API surface that the MCP
// transport (T12) and any future gRPC/HTTP transport call into. T15 owns the
// canonical Service struct + constructor; later tickets append their own
// fields and constructor wiring without replacing the type.
package svc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"tickets_please/internal/cache"
	"tickets_please/internal/config"
	"tickets_please/internal/store"
)

// Service is the in-process API surface. T15 declares the foundational
// fields; later tickets add their own:
//
//	Cache         *cache.ProjectCache  // T04
//	Embed         embed.Provider       // T08 wired by T10
//	Worker        *worker.Worker       // T10
//	LearningsIdx  *vecindex.Index      // T11
//	SummaryIdx    *vecindex.Index      // T11
type Service struct {
	Store  *store.Store
	Logger *slog.Logger
	Cfg    config.Config

	// Cache is the in-memory project cache (T04). Lazy-loads project trees
	// off disk, sliding-TTL evicts, and listens for cross-process file
	// changes via fsnotify.
	Cache *cache.ProjectCache

	// cacheCancel stops the background eviction goroutine. Held so tests
	// (and future graceful-shutdown paths) can tear it down.
	cacheCancel context.CancelFunc

	// Agent debounce state. touchOnce tracks the last time we rewrote
	// LastSeenAt for a given agent id; touchMu guards the map.
	touchOnce map[string]time.Time
	touchMu   sync.Mutex
}

// New builds a Service: resolves the data dir into a *store.Store, wires a
// JSON-handler slog logger pointed at stderr, builds the project cache, and
// kicks off its background eviction loop. Later tickets extend this
// constructor with their own field construction (embed provider, worker,
// vec indexes, ...).
func New(cfg config.Config) (*Service, error) {
	st, err := store.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("svc: build store: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	pc := cache.New(st, cfg)
	pc.Logger = logger

	evictCtx, cancel := context.WithCancel(context.Background())
	s := &Service{
		Store:       st,
		Logger:      logger,
		Cfg:         cfg,
		Cache:       pc,
		cacheCancel: cancel,
		touchOnce:   make(map[string]time.Time),
	}
	go s.Cache.RunEvictor(evictCtx)
	return s, nil
}

// Close stops background goroutines (currently the cache evictor) and
// releases all watcher resources. Safe to call multiple times.
func (s *Service) Close() {
	if s.cacheCancel != nil {
		s.cacheCancel()
		s.cacheCancel = nil
	}
	if s.Cache != nil {
		s.Cache.CloseAll()
	}
}
