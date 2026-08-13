package middleware

import (
	"testing"
	"time"
)

// TestShutdownSignal: long-lived handlers (the /api/events SSE stream)
// select on ShutdownSignal so the connection ends promptly when the app
// quits — otherwise http.Server.Shutdown waits the whole grace period for a
// stream that would otherwise live forever.
//
// BeginShutdown is once-per-process by design, so the test must be
// idempotent: re-running it (go test -count=N) in the same process must not
// fail. The meaningful invariants are that after BeginShutdown the signal
// is closed, and that a second call does not panic (sync.Once semantics).
func TestShutdownSignal(t *testing.T) {
	BeginShutdown()
	select {
	case <-ShutdownSignal():
	case <-time.After(time.Second):
		t.Fatal("shutdown signal not closed after BeginShutdown")
	}
	BeginShutdown()
}
