// Package sse is the realtime fan-out for the web UI. A single in-process
// Hub multiplexes Events from producers (svc mutations, dev pings) to
// SSE-streaming HTTP handlers subscribed by topic.
//
// Wave 1 of phase 011 lands the wiring with one global topic and no
// producers; later waves split the topic space per-session (see ticket
// sse-hub-per-session-topic-scoped) and start publishing on real mutations.
package sse

import (
	"sync"
)

// Event is one SSE message to deliver downstream. Fields map onto the SSE
// framing the handler emits:
//
//	id:    <ID>          (omitted when empty)
//	event: <Type>        (omitted when empty — clients default to "message")
//	data:  <each line of Data>
//
// Data is the already-formatted payload body. For Datastar it's typically
// "elements <html>" with optional "selector …" / "mode …" lines; callers own
// the formatting so the Hub stays content-agnostic.
type Event struct {
	Type string
	Data string
	ID   string
}

// Hub is the contract the SSE handler depends on. Concrete impls live in this
// package; the interface is here so test doubles can stand in without
// touching real channels.
type Hub interface {
	// Subscribe returns a receive-only channel for events on topic and a
	// cancel func the caller MUST invoke when done (on context cancel or
	// connection close). The channel is closed by Unsubscribe; ranging over
	// it without calling cancel leaks a buffered chan + a map entry per dead
	// subscriber.
	Subscribe(topic string) (<-chan Event, func())
	// Publish delivers ev to every subscriber of topic. Non-blocking per
	// subscriber: a slow consumer that has filled its buffer is skipped for
	// this one event rather than holding up the publisher. Slow consumers
	// observably drop events; that's the price of keeping mutations cheap.
	Publish(topic string, ev Event)
}

// MemHub is the in-process Hub implementation. One mutex guards the topic →
// subscribers map; deliveries themselves go through buffered channels so a
// burst of N events doesn't block the publisher while subscribers drain.
//
// Buffer size is a deliberate trade-off: too small drops events under any
// jitter, too large hides slow-consumer bugs. 16 fits the W3 mutation pattern
// (one ticket move ≈ one event) with headroom for a burst.
type MemHub struct {
	mu   sync.Mutex
	subs map[string]map[chan Event]struct{}
}

const subBuffer = 16

// NewMemHub returns a ready-to-use in-process Hub.
func NewMemHub() *MemHub {
	return &MemHub{subs: make(map[string]map[chan Event]struct{})}
}

// Subscribe creates a buffered chan for topic and a cancel func that
// unregisters and closes it. Safe to call from many goroutines.
func (h *MemHub) Subscribe(topic string) (<-chan Event, func()) {
	ch := make(chan Event, subBuffer)
	h.mu.Lock()
	bucket, ok := h.subs[topic]
	if !ok {
		bucket = make(map[chan Event]struct{})
		h.subs[topic] = bucket
	}
	bucket[ch] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		if bucket, ok := h.subs[topic]; ok {
			if _, present := bucket[ch]; present {
				delete(bucket, ch)
				close(ch)
				if len(bucket) == 0 {
					delete(h.subs, topic)
				}
			}
		}
		h.mu.Unlock()
	}
	return ch, cancel
}

// Publish fans ev out to every current subscriber of topic. A subscriber
// whose buffer is full is skipped (drops the event for that one consumer)
// rather than blocking — see the MemHub doc on the slow-consumer trade-off.
func (h *MemHub) Publish(topic string, ev Event) {
	h.mu.Lock()
	bucket := h.subs[topic]
	// Copy targets so we can release the lock before sending.
	targets := make([]chan Event, 0, len(bucket))
	for ch := range bucket {
		targets = append(targets, ch)
	}
	h.mu.Unlock()
	for _, ch := range targets {
		select {
		case ch <- ev:
		default:
		}
	}
}
