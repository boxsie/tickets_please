package sse

import (
	"sync"
	"testing"
	"time"
)

func TestMemHub_SubscribePublishReceive(t *testing.T) {
	h := NewMemHub()
	ch, cancel := h.Subscribe("global")
	defer cancel()

	want := Event{Type: "datastar-patch-elements", Data: "elements <span id=\"x\">hi</span>"}
	h.Publish("global", want)

	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("received event mismatch: got %#v want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for published event")
	}
}

func TestMemHub_TopicIsolation(t *testing.T) {
	h := NewMemHub()
	chA, cancelA := h.Subscribe("a")
	chB, cancelB := h.Subscribe("b")
	defer cancelA()
	defer cancelB()

	h.Publish("a", Event{Type: "x", Data: "for-a"})

	select {
	case got := <-chA:
		if got.Data != "for-a" {
			t.Fatalf("a got wrong payload: %q", got.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("a never received its event")
	}

	select {
	case got := <-chB:
		t.Fatalf("b leaked an event from topic a: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMemHub_FanOutToMultipleSubscribers(t *testing.T) {
	h := NewMemHub()
	const n = 5
	chans := make([]<-chan Event, n)
	cancels := make([]func(), n)
	for i := range n {
		chans[i], cancels[i] = h.Subscribe("global")
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	h.Publish("global", Event{Data: "broadcast"})

	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			select {
			case got := <-chans[i]:
				if got.Data != "broadcast" {
					t.Errorf("subscriber %d got %q", i, got.Data)
				}
			case <-time.After(time.Second):
				t.Errorf("subscriber %d timed out", i)
			}
		}(i)
	}
	wg.Wait()
}

func TestMemHub_UnsubscribeStopsDelivery(t *testing.T) {
	h := NewMemHub()
	ch, cancel := h.Subscribe("global")
	cancel()

	// Re-subscribe a fresh listener so the publish actually goes to someone
	// (the bucket got pruned on the cancel above).
	keep, keepCancel := h.Subscribe("global")
	defer keepCancel()

	h.Publish("global", Event{Data: "after-cancel"})

	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("cancelled subscriber still received an event: %#v", ev)
		}
		// ok=false means the channel was closed by cancel — that's the contract.
	case <-time.After(50 * time.Millisecond):
		// Fine: cancelled subscriber stays silent and eventually GC'd.
	}

	select {
	case got := <-keep:
		if got.Data != "after-cancel" {
			t.Fatalf("kept subscriber got %q", got.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("kept subscriber missed the event")
	}
}

func TestMemHub_SlowConsumerIsSkippedNotBlocking(t *testing.T) {
	h := NewMemHub()
	// Subscribe but never read — channel buffer (subBuffer) fills, further
	// publishes for this subscriber must drop instead of blocking publishers.
	_, cancel := h.Subscribe("global")
	defer cancel()

	done := make(chan struct{})
	go func() {
		for range subBuffer * 4 {
			h.Publish("global", Event{Data: "spam"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher blocked on a slow consumer — drop-on-full broken")
	}
}

func TestMemHub_PublishToTopicWithNoSubscribersIsNoop(t *testing.T) {
	h := NewMemHub()
	// Just must not panic / hang.
	h.Publish("empty", Event{Data: "nobody home"})
}
