package events

import (
	"testing"
	"time"
)

// TestHubBroadcastsToAllSubscribers: every subscribed consumer (each open
// SSE connection) must receive every published event.
func TestHubBroadcastsToAllSubscribers(t *testing.T) {
	h := NewHub()
	a := h.Subscribe()
	b := h.Subscribe()
	defer h.Unsubscribe(a)
	defer h.Unsubscribe(b)

	h.Publish(Event{Type: TypeKeyStatusChanged, KeyID: 7, Status: "disabled"})

	select {
	case e := <-a:
		if e.Type != TypeKeyStatusChanged || e.KeyID != 7 || e.Status != "disabled" {
			t.Fatalf("subscriber a got %+v, want key_status_changed key 7 disabled", e)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber a never received the event")
	}
	select {
	case <-b:
	case <-time.After(time.Second):
		t.Fatal("subscriber b never received the event")
	}
}

// TestHubSlowSubscriberDoesNotBlock: Publish must never block on a
// subscriber that is not reading (a dead SSE connection). The slow
// subscriber misses events; the fast one still gets them.
func TestHubSlowSubscriberDoesNotBlock(t *testing.T) {
	h := NewHub()
	slow := h.Subscribe()
	fast := h.Subscribe()
	defer h.Unsubscribe(slow)
	defer h.Unsubscribe(fast)

	// Fill the slow subscriber's buffer without reading from it.
	for i := 0; i < 1000; i++ {
		h.Publish(Event{Type: TypeKeyStatusChanged, KeyID: int64(i), Status: "rate_limited"})
	}

	// The hub must still deliver to the reader.
	select {
	case e := <-fast:
		if e.KeyID != 0 {
			t.Fatalf("fast subscriber got key %d, want the first event (key 0)", e.KeyID)
		}
	case <-time.After(time.Second):
		t.Fatal("fast subscriber blocked behind the slow one")
	}
}

// TestHubUnsubscribeStopsDelivery: after Unsubscribe, no further events may
// reach the channel (the SSE handler relies on this to stop writing to a
// closed connection).
func TestHubUnsubscribeStopsDelivery(t *testing.T) {
	h := NewHub()
	ch := h.Subscribe()
	h.Unsubscribe(ch)

	h.Publish(Event{Type: TypeKeyStatusChanged, KeyID: 1})
	select {
	case e := <-ch:
		t.Fatalf("unsubscribed channel received %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}
