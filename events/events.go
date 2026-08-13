// Package events implements a tiny publish/subscribe hub used to push
// change notifications from the backend to the UI over Server-Sent Events
// (GET /api/events). The UI treats an event as "something changed, go
// re-fetch it" — a hot reload that updates in place instead of the UI
// polling every resource on a timer.
package events

import "sync"

// Event is a change notification pushed to subscribed clients.
type Event struct {
	// Type identifies what changed. Currently: "key_status_changed".
	Type string `json:"type"`
	// KeyID is the affected key (set for key_status_changed).
	KeyID int64 `json:"key_id,omitempty"`
	// Status is the key's new status (set for key_status_changed).
	Status string `json:"status,omitempty"`
}

// Event types.
const (
	// TypeKeyStatusChanged is published after a key's status flips in the
	// DB (rate_limited / disabled / active) — the relay and health checker
	// flip keys in the background, and the UI must re-fetch the key list.
	TypeKeyStatusChanged = "key_status_changed"
)

// Hub fans events out to subscribed channels.
type Hub struct {
	mu     sync.RWMutex
	subs   map[chan Event]struct{}
	bufLen int
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan Event]struct{}), bufLen: 16}
}

// Subscribe registers a new subscriber channel. The returned channel is
// owned by the caller; Unsubscribe removes it.
func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, h.bufLen)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel. The channel is NOT closed:
// closing would make it immediately readable (zero values) to any goroutine
// still selecting on it. The subscriber is the only reader and stops
// reading before unsubscribing, so the buffered channel is simply garbage
// collected once the subscriber is done.
func (h *Hub) Unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// Len returns the number of active subscribers (used by tests to wait for a
// handler to be ready).
func (h *Hub) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// Publish delivers an event to every subscriber without ever blocking. A
// subscriber that cannot keep up (slow client, disconnect in progress)
// simply misses the event — the UI re-fetches authoritative data anyway, so
// dropping is safe and a stalled client must never stall the app.
func (h *Hub) Publish(e Event) {
	h.mu.RLock()
	subs := make([]chan Event, 0, len(h.subs))
	for ch := range h.subs {
		subs = append(subs, ch)
	}
	h.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
		}
	}
}
