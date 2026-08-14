package middleware

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// setStopping forces the shutdown switch without going through
// BeginShutdown, so tests stay independent of one another's ordering (and
// of -count=N reruns). BeginShutdown itself is once-per-process by design.
func setStopping(v bool) {
	shutdownState.Lock()
	shutdownState.stopping = v
	shutdownState.Unlock()
}

// TestShutdownSignal: long-lived handlers (the /api/events SSE stream)
// select on ShutdownSignal so the connection ends promptly when the app
// quits — otherwise the drain would block for a stream that would
// otherwise live forever.
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

// newEngine builds a gin engine mirroring the production middleware chain
// (Recovery BEFORE the shutdown middleware — the order router.go uses),
// served over a real TCP socket so the hijack path is real. handlerRan is
// set if the API route handler ever runs (nil to ignore). The client gets a
// timeout so a hang regression fails the test instead of hanging it.
func newEngine(handlerRan *atomic.Bool) *httptest.Server {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(ShutdownMiddleware())
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		if handlerRan != nil {
			handlerRan.Store(true)
		}
		c.String(http.StatusOK, "must not be served")
	})
	r.GET("/api/health", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	srv := httptest.NewServer(r)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	return srv
}

// newClient returns an HTTP client with a timeout, so a regression that
// hangs requests fails the test instead of hanging the suite.
func newClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

// TestShutdownMiddlewareClosesNewRequests: once shutdown has begun, new
// requests must NOT get a response — the connection is closed instead, so
// the client sees a connection failure (the one failure mode every agent
// and SDK auto-retries). The route handler must not run either: a dead
// client must not consume upstream quota.
func TestShutdownMiddlewareClosesNewRequests(t *testing.T) {
	setStopping(true)
	var handlerRan atomic.Bool
	srv := newEngine(&handlerRan)
	defer srv.Close()

	resp, err := newClient().Post(srv.URL+"/v1/chat/completions", "application/json", nil)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("request got a response (status %d), want connection error", resp.StatusCode)
	}
	if handlerRan.Load() {
		t.Error("route handler ran for a request refused during shutdown")
	}
}

// TestShutdownMiddlewareNormalRequestsPass: before shutdown begins, the
// middleware must not interfere with normal requests.
func TestShutdownMiddlewareNormalRequestsPass(t *testing.T) {
	setStopping(false)
	var handlerRan atomic.Bool
	srv := newEngine(&handlerRan)
	defer srv.Close()

	resp, err := newClient().Post(srv.URL+"/v1/chat/completions", "application/json", nil)
	if err != nil {
		t.Fatalf("request failed before shutdown: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !handlerRan.Load() {
		t.Error("route handler did not run for a normal request")
	}
}

// TestShutdownMiddlewareKeepsHealthDuringShutdown: the health endpoint must
// keep answering during the drain so the updater/probes can still see the
// process.
func TestShutdownMiddlewareKeepsHealthDuringShutdown(t *testing.T) {
	setStopping(true)
	srv := newEngine(nil)
	defer srv.Close()

	resp, err := newClient().Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatalf("health request failed during shutdown: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health during shutdown = %d, want 200", resp.StatusCode)
	}
}
