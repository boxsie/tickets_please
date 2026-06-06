// Package eventbus is the typed, in-process realtime backbone for the web UI.
//
// The service layer publishes typed domain Events (a ticket moved, a comment
// landed, an agent registered) into a Bus, fire-and-forget. The web layer
// subscribes per SSE connection to a set of topics, drains any buffered replay
// (for Last-Event-ID reconnects), then attaches for live deliveries which it
// projects into Datastar DOM patches.
//
// The Bus owns event identity: every published Event is stamped with a
// process-monotonic Seq. The Bus keeps a bounded per-topic ring buffer so a
// briefly-disconnected client can resume from its last seen Seq without a full
// reload. Publishing never blocks on a slow consumer — a subscriber whose
// buffer fills is disconnected (its done channel closes) and is expected to
// reconnect with Last-Event-ID.
//
// This package has no HTTP or HTML concerns: payloads are pure data. The
// web/sse package renders Events into SSE frames; svc only ever sees the
// Publisher interface so it stays free of any web coupling.
package eventbus

import (
	"sort"
	"sync"
	"time"
)

// Kind discriminates the Event payload. One flat Event struct (rather than an
// interface hierarchy) keeps publishing, testing, and the web-side switch
// trivial — only a handful of fields are ever populated per Kind.
type Kind string

const (
	KindTicketCreated    Kind = "ticket_created"
	KindTicketMoved      Kind = "ticket_moved"
	KindTicketCompleted  Kind = "ticket_completed"
	KindCommentAdded     Kind = "comment_added"
	KindTicketArchived   Kind = "ticket_archived"
	KindTicketUnarchived Kind = "ticket_unarchived"
	KindAgentRegistered  Kind = "agent_registered"
	KindAgentSeen        Kind = "agent_seen"
)

// Topic constructors. Topic shapes are documented on ticket #80:
//
//	project:{id}   ticket:{id}   phase:{id}   agent:{id}   global:agents
func TopicProject(id string) string { return "project:" + id }
func TopicTicket(id string) string  { return "ticket:" + id }
func TopicPhase(id string) string   { return "phase:" + id }
func TopicAgent(id string) string   { return "agent:" + id }

// TopicGlobalAgents carries agent-registry events (registrations, last-seen
// ticks) that aren't scoped to any one project.
const TopicGlobalAgents = "global:agents"

// Event is one typed domain event. Seq is assigned by the Bus on Publish and
// is zero before. Topics is the fan-out set the publisher chose; the same
// Event is delivered once per matching subscribed topic (the matched topic
// travels with it in a Delivery so the renderer knows which surface to patch).
type Event struct {
	Seq    uint64
	Kind   Kind
	Topics []string

	// Ticket-scoped (Moved / Completed / Archived / Unarchived).
	TicketID   string
	ProjectID  string
	PhaseID    string
	FromColumn string
	ToColumn   string

	// Comment-scoped (CommentAdded; also set for the system_* comment a
	// move/complete/archive emits, when the caller wants the thread patched).
	CommentID   string
	CommentKind string

	// Actor — who performed the mutation.
	ByAgentID   string
	ByAgentName string
	ByUserID    string
	ByUserName  string

	// Agent-registry-scoped (Registered / Seen).
	AgentID    string
	AgentName  string
	UserID     string
	LastSeenAt time.Time
}

// Delivery pairs an Event with the specific subscribed topic that matched it,
// so a phase-detail connection (subscribed to phase:+project:) can tell which
// scope fired and render the right patch.
type Delivery struct {
	Topic string
	Event Event
}

// Publisher is the write side svc depends on. Publish is fire-and-forget and
// MUST NOT block the caller's mutation; a nil Publisher is a no-op via the
// Nop helper so the service layer needn't nil-check at every call site.
type Publisher interface {
	Publish(ev Event)
}

// Nop is a Publisher that discards everything. Used when realtime is unwired
// (e.g. stdio MCP mode, or tests that don't care about events).
type Nop struct{}

func (Nop) Publish(Event) {}

const (
	// ringSize is the per-topic replay history. 1024 covers a generous
	// reconnect window; beyond it a client is better off full-reloading.
	ringSize = 1024
	// liveBuffer is the per-subscription live channel depth. A consumer that
	// can't keep up past this is disconnected (see Publish) rather than
	// throttling the publisher.
	liveBuffer = 64
)

type subscription struct {
	topics map[string]struct{}
	ch     chan Delivery
	done   chan struct{}
	once   sync.Once
}

func (s *subscription) close() { s.once.Do(func() { close(s.done) }) }

// Bus is the in-process event hub: monotonic Seq allocation, per-topic ring
// buffers for replay, and topic-scoped live fan-out. Safe for concurrent use.
type Bus struct {
	mu    sync.Mutex
	seq   uint64
	rings map[string][]Event
	subs  map[*subscription]struct{}
}

// NewBus returns a ready Bus.
func NewBus() *Bus {
	return &Bus{
		rings: make(map[string][]Event),
		subs:  make(map[*subscription]struct{}),
	}
}

// Publish stamps ev with the next Seq, appends it to each topic's ring buffer,
// and delivers it to every live subscriber of a matching topic. Delivery is
// non-blocking: a subscriber whose buffer is full is disconnected (its done
// channel closes) instead of stalling the publisher.
func (b *Bus) Publish(ev Event) {
	if len(ev.Topics) == 0 {
		return
	}
	b.mu.Lock()
	b.seq++
	ev.Seq = b.seq
	for _, t := range ev.Topics {
		r := append(b.rings[t], ev)
		if len(r) > ringSize {
			r = r[len(r)-ringSize:]
		}
		b.rings[t] = r
	}
	type target struct {
		s     *subscription
		topic string
	}
	var targets []target
	for s := range b.subs {
		for _, t := range ev.Topics {
			if _, ok := s.topics[t]; ok {
				targets = append(targets, target{s, t})
				break
			}
		}
	}
	b.mu.Unlock()

	for _, t := range targets {
		select {
		case <-t.s.done:
			// already disconnected; the handler's deferred cancel reaps it.
		case t.s.ch <- Delivery{Topic: t.topic, Event: ev}:
		default:
			// Slow consumer: signal disconnect. The handler observes done,
			// returns, and reconnects with Last-Event-ID to resume.
			t.s.close()
		}
	}
}

// Subscribe registers a live subscription for the given topics and atomically
// snapshots any buffered events newer than lastSeq for replay. The returned
// replay slice is sorted by Seq and contains no event that the live channel
// will also deliver (the snapshot and registration happen under one lock, so
// there's no gap and no overlap with subsequently-published events).
//
// The caller MUST invoke cancel when done (typically deferred) to drop the
// subscription. done closes when the Bus disconnects a slow consumer; callers
// should select on it and stop reading ch.
func (b *Bus) Subscribe(topics []string, lastSeq uint64) (replay []Delivery, ch <-chan Delivery, done <-chan struct{}, cancel func()) {
	s := &subscription{
		topics: make(map[string]struct{}, len(topics)),
		ch:     make(chan Delivery, liveBuffer),
		done:   make(chan struct{}),
	}
	for _, t := range topics {
		s.topics[t] = struct{}{}
	}

	b.mu.Lock()
	if lastSeq > 0 {
		seen := make(map[uint64]struct{})
		for _, t := range topics {
			for _, ev := range b.rings[t] {
				if ev.Seq <= lastSeq {
					continue
				}
				if _, dup := seen[ev.Seq]; dup {
					continue
				}
				seen[ev.Seq] = struct{}{}
				replay = append(replay, Delivery{Topic: t, Event: ev})
			}
		}
		sort.Slice(replay, func(i, j int) bool {
			return replay[i].Event.Seq < replay[j].Event.Seq
		})
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	cancel = func() {
		s.close()
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	}
	return replay, s.ch, s.done, cancel
}

// SubscriberCount reports how many live subscriptions are currently
// registered. Test/introspection helper.
func (b *Bus) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
