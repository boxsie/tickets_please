// Package log provides an in-process slog.Handler that captures the most
// recent records into a fixed-size ring buffer. Pair it with a regular
// stderr handler via NewMultiHandler so /logs in the web UI mirrors what
// the operator sees on stderr.
package log

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
)

// DefaultCapacity is the ring's record capacity when no override is given.
// 2000 lines × ~500 B/line ≈ 1 MB. Cheap, plenty for a hobby debug page.
const DefaultCapacity = 2000

// Ring stores up to N JSON-encoded slog records. New writes evict the
// oldest. Reads return entries oldest-first.
type Ring struct {
	mu  sync.Mutex
	buf [][]byte
	cap int
	// head is the index of the next slot to write. When the ring is full,
	// head also points at the oldest entry (about to be overwritten).
	head int
	// full indicates the ring has wrapped at least once.
	full bool
}

// NewRing builds a Ring with the given capacity. capacity <= 0 falls back
// to DefaultCapacity.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Ring{
		buf: make([][]byte, capacity),
		cap: capacity,
	}
}

// Append records a single line (a complete JSON-encoded slog record). The
// slice is copied so the caller can reuse its buffer.
func (r *Ring) Append(line []byte) {
	cp := make([]byte, len(line))
	copy(cp, line)
	r.mu.Lock()
	r.buf[r.head] = cp
	r.head++
	if r.head >= r.cap {
		r.head = 0
		r.full = true
	}
	r.mu.Unlock()
}

// Snapshot returns a copy of the ring's contents oldest-first. Each entry
// is one JSON-encoded record (no trailing newline).
func (r *Ring) Snapshot() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]byte
	if r.full {
		out = make([][]byte, 0, r.cap)
		out = append(out, r.buf[r.head:]...)
		out = append(out, r.buf[:r.head]...)
	} else {
		out = make([][]byte, r.head)
		copy(out, r.buf[:r.head])
	}
	return out
}

// RingHandler is a slog.Handler that mirrors every record into a Ring as
// JSON-encoded bytes. It does NOT delegate to another handler — compose
// with MultiHandler when you also want stderr output.
type RingHandler struct {
	ring  *Ring
	inner *slog.JSONHandler
	buf   *bytes.Buffer
	mu    *sync.Mutex
}

// NewRingHandler builds a Handler that writes each record as one JSON line
// into ring. Level/attrs follow the supplied opts (nil → default).
func NewRingHandler(ring *Ring, opts *slog.HandlerOptions) *RingHandler {
	buf := &bytes.Buffer{}
	mu := &sync.Mutex{}
	return &RingHandler{
		ring:  ring,
		inner: slog.NewJSONHandler(buf, opts),
		buf:   buf,
		mu:    mu,
	}
}

// Enabled defers to the inner JSON handler's level filter.
func (h *RingHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.inner.Enabled(ctx, lvl)
}

// Handle encodes the record as JSON and appends it to the ring.
func (h *RingHandler) Handle(ctx context.Context, rec slog.Record) error {
	h.mu.Lock()
	h.buf.Reset()
	err := h.inner.Handle(ctx, rec)
	line := bytes.TrimRight(h.buf.Bytes(), "\n")
	cp := make([]byte, len(line))
	copy(cp, line)
	h.mu.Unlock()
	if err != nil {
		return err
	}
	h.ring.Append(cp)
	return nil
}

// WithAttrs returns a handler whose JSON encoder carries the extra attrs.
func (h *RingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &RingHandler{
		ring:  h.ring,
		inner: h.inner.WithAttrs(attrs).(*slog.JSONHandler),
		buf:   h.buf,
		mu:    h.mu,
	}
}

// WithGroup returns a handler whose JSON encoder opens a group.
func (h *RingHandler) WithGroup(name string) slog.Handler {
	return &RingHandler{
		ring:  h.ring,
		inner: h.inner.WithGroup(name).(*slog.JSONHandler),
		buf:   h.buf,
		mu:    h.mu,
	}
}

// MultiHandler fans every record out to its underlying handlers. Errors
// from individual handlers are joined into one returned error rather than
// short-circuiting — losing a stderr write shouldn't drop the ring entry.
type MultiHandler struct {
	hs []slog.Handler
}

// NewMultiHandler wraps zero or more handlers as one. nil entries are dropped.
func NewMultiHandler(hs ...slog.Handler) *MultiHandler {
	out := make([]slog.Handler, 0, len(hs))
	for _, h := range hs {
		if h != nil {
			out = append(out, h)
		}
	}
	return &MultiHandler{hs: out}
}

// Enabled returns true if any inner handler is enabled at lvl.
func (m *MultiHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	for _, h := range m.hs {
		if h.Enabled(ctx, lvl) {
			return true
		}
	}
	return false
}

// Handle dispatches to every inner handler that's enabled.
func (m *MultiHandler) Handle(ctx context.Context, rec slog.Record) error {
	var firstErr error
	for _, h := range m.hs {
		if !h.Enabled(ctx, rec.Level) {
			continue
		}
		if err := h.Handle(ctx, rec.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// WithAttrs propagates the attrs to every inner handler.
func (m *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(m.hs))
	for i, h := range m.hs {
		out[i] = h.WithAttrs(attrs)
	}
	return &MultiHandler{hs: out}
}

// WithGroup propagates the group to every inner handler.
func (m *MultiHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(m.hs))
	for i, h := range m.hs {
		out[i] = h.WithGroup(name)
	}
	return &MultiHandler{hs: out}
}
