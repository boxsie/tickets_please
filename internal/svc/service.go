// Package svc implements the in-process Service API surface that the MCP
// transport (T12) and any future gRPC/HTTP transport call into. T15 owns the
// canonical Service struct + constructor; later tickets append their own
// fields and constructor wiring without replacing the type.
package svc

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

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

	// Agent debounce state. touchOnce tracks the last time we rewrote
	// LastSeenAt for a given agent id; touchMu guards the map.
	touchOnce map[string]time.Time
	touchMu   sync.Mutex
}

// New builds a Service: resolves the data dir into a *store.Store, wires a
// JSON-handler slog logger pointed at stderr, and returns the foundational
// shape. Later tickets extend this constructor with their own field
// construction (cache, embed provider, worker, vec indexes, ...).
func New(cfg config.Config) (*Service, error) {
	st, err := store.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("svc: build store: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	return &Service{
		Store:     st,
		Logger:    logger,
		Cfg:       cfg,
		touchOnce: make(map[string]time.Time),
	}, nil
}
