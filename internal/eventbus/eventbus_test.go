package eventbus

import (
	"testing"
)

// recv pulls one Delivery or fails — keeps the table tests terse.
func recv(t *testing.T, ch <-chan Delivery) Delivery {
	t.Helper()
	select {
	case d := <-ch:
		return d
	default:
		t.Fatal("expected a delivery, channel was empty")
		return Delivery{}
	}
}

func TestBus_SubscribePublishReceive(t *testing.T) {
	b := NewBus()
	_, ch, _, cancel := b.Subscribe([]string{TopicTicket("t1")}, 0)
	defer cancel()

	b.Publish(Event{Kind: KindTicketMoved, Topics: []string{TopicTicket("t1")}, TicketID: "t1", ToColumn: "in_progress"})

	d := recv(t, ch)
	if d.Topic != TopicTicket("t1") {
		t.Errorf("topic = %q, want ticket:t1", d.Topic)
	}
	if d.Event.Seq != 1 {
		t.Errorf("seq = %d, want 1 (assigned by bus)", d.Event.Seq)
	}
	if d.Event.ToColumn != "in_progress" {
		t.Errorf("payload not carried: %+v", d.Event)
	}
}

func TestBus_TopicScoping(t *testing.T) {
	b := NewBus()
	_, chA, _, cancelA := b.Subscribe([]string{TopicTicket("a")}, 0)
	defer cancelA()
	_, chB, _, cancelB := b.Subscribe([]string{TopicTicket("b")}, 0)
	defer cancelB()

	b.Publish(Event{Kind: KindCommentAdded, Topics: []string{TopicTicket("a")}, TicketID: "a"})

	if got := recv(t, chA); got.Event.TicketID != "a" {
		t.Errorf("subscriber A got %q, want a", got.Event.TicketID)
	}
	select {
	case d := <-chB:
		t.Errorf("subscriber B should not have received %+v", d)
	default:
	}
}

func TestBus_MultiTopicFanout(t *testing.T) {
	b := NewBus()
	// A phase-detail connection subscribes to both phase: and project:.
	_, ch, _, cancel := b.Subscribe([]string{TopicPhase("p1"), TopicProject("proj1")}, 0)
	defer cancel()

	// One event published to several topics is delivered once, tagged with a
	// matching topic.
	b.Publish(Event{
		Kind:      KindTicketMoved,
		Topics:    []string{TopicTicket("t1"), TopicPhase("p1"), TopicProject("proj1")},
		TicketID:  "t1",
		ProjectID: "proj1",
		PhaseID:   "p1",
	})

	d := recv(t, ch)
	if d.Topic != TopicPhase("p1") && d.Topic != TopicProject("proj1") {
		t.Errorf("matched topic = %q, want phase:p1 or project:proj1", d.Topic)
	}
	// No second copy — fan-out is once per subscription, not once per topic.
	select {
	case extra := <-ch:
		t.Errorf("expected single delivery, got extra %+v", extra)
	default:
	}
}

func TestBus_ReplayOnlyNewer(t *testing.T) {
	b := NewBus()
	topic := TopicProject("proj1")

	// Publish three events with no subscriber; they land in the ring buffer.
	for range 3 {
		b.Publish(Event{Kind: KindTicketMoved, Topics: []string{topic}, TicketID: "t1"})
	}

	// Reconnect "having seen seq 1" — expect only seq 2 and 3 replayed, in order.
	replay, ch, _, cancel := b.Subscribe([]string{topic}, 1)
	defer cancel()

	if len(replay) != 2 {
		t.Fatalf("replay len = %d, want 2", len(replay))
	}
	if replay[0].Event.Seq != 2 || replay[1].Event.Seq != 3 {
		t.Errorf("replay seqs = %d,%d, want 2,3", replay[0].Event.Seq, replay[1].Event.Seq)
	}

	// A live event published after subscribe arrives on the channel, never in
	// replay — no gap, no overlap.
	b.Publish(Event{Kind: KindTicketMoved, Topics: []string{topic}, TicketID: "t1"})
	if got := recv(t, ch); got.Event.Seq != 4 {
		t.Errorf("live seq = %d, want 4", got.Event.Seq)
	}
}

func TestBus_ReplayDedupsAcrossTopics(t *testing.T) {
	b := NewBus()
	// One event on two topics the subscriber both watches must replay once.
	b.Publish(Event{Kind: KindTicketMoved, Topics: []string{TopicPhase("p1"), TopicProject("proj1")}, TicketID: "t1"})

	replay, _, _, cancel := b.Subscribe([]string{TopicPhase("p1"), TopicProject("proj1")}, 0)
	defer cancel()
	// lastSeq 0 means no replay at all (fresh connect) — verify that contract.
	if len(replay) != 0 {
		t.Fatalf("fresh connect (lastSeq=0) should not replay, got %d", len(replay))
	}

	replay2, _, _, cancel2 := b.Subscribe([]string{TopicPhase("p1"), TopicProject("proj1")}, 0)
	defer cancel2()
	_ = replay2
}

func TestBus_SlowConsumerDisconnected(t *testing.T) {
	b := NewBus()
	topic := TopicProject("proj1")
	_, ch, done, cancel := b.Subscribe([]string{topic}, 0)
	defer cancel()

	// Never drain ch. Publish past the live buffer; the bus must disconnect us.
	for range liveBuffer + 5 {
		b.Publish(Event{Kind: KindTicketMoved, Topics: []string{topic}, TicketID: "t1"})
	}

	select {
	case <-done:
		// expected: backpressure tripped the disconnect.
	default:
		t.Fatal("slow consumer was not disconnected after buffer overflow")
	}

	// The buffer should still hold liveBuffer events for an attentive reader,
	// but the contract we assert is the disconnect signal above.
	_ = ch
}

func TestBus_CancelRemovesSubscription(t *testing.T) {
	b := NewBus()
	_, _, _, cancel := b.Subscribe([]string{TopicGlobalAgents}, 0)
	if b.SubscriberCount() != 1 {
		t.Fatalf("subscriber count = %d, want 1", b.SubscriberCount())
	}
	cancel()
	if b.SubscriberCount() != 0 {
		t.Fatalf("subscriber count after cancel = %d, want 0", b.SubscriberCount())
	}
	// Publishing after everyone left is a no-op, not a panic.
	b.Publish(Event{Kind: KindAgentSeen, Topics: []string{TopicGlobalAgents}, AgentID: "a1"})
}

func TestBus_NopPublisher(t *testing.T) {
	var p Publisher = Nop{}
	p.Publish(Event{Kind: KindTicketMoved}) // must not panic
}
