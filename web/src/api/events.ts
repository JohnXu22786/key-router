// Live push channel: the backend publishes change notifications over
// Server-Sent Events (GET /api/events); pages subscribe and re-fetch the
// affected resource on demand. This is the "hot reload" contract — update
// in place when something actually changed, no timer-driven re-render and
// no full-page reload.
//
// A single shared EventSource serves all subscribers. EventSource
// reconnects automatically when the connection drops, so a missed event
// (e.g. during the gap) is picked up by the pages' fallback poll.
export interface SseEvent {
  type: string;
  key_id?: number;
  status?: string;
}

type Listener = (e: SseEvent) => void;

let es: EventSource | null = null;
const listeners = new Set<Listener>();

function ensureConnected() {
  if (es) return;
  es = new EventSource('/api/events');
  es.onmessage = (msg) => {
    let e: SseEvent;
    try { e = JSON.parse(msg.data); } catch { return; }
    listeners.forEach(fn => fn(e));
  };
  // Reconnection is automatic; onerror only needs to exist so connection
  // errors are not surfaced as unhandled errors.
  es.onerror = () => {};
}

// subscribeEvents registers a listener and returns an unsubscribe function.
// The shared connection is torn down when the last subscriber leaves.
export function subscribeEvents(fn: Listener): () => void {
  ensureConnected();
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
    if (listeners.size === 0 && es) {
      es.close();
      es = null;
    }
  };
}

// jsonEqual compares two values structurally. Pages use it to apply fetched
// data only when it actually changed — React then skips re-rendering
// entirely when a poll or push returned nothing new, so unchanged data
// never causes visible flicker.
export function jsonEqual<T>(a: T, b: T): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}
