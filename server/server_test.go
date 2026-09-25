package server

import (
	"embed"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"key-router/events"
	"key-router/middleware"
	"key-router/router"

	"github.com/gin-gonic/gin"
)

func TestAppBeginShutdownPropagatesToRouter(t *testing.T) {
	if middleware.IsStopping() {
		t.Skip("middleware shutdown state is already set")
	}

	hub := events.NewHub()
	r := router.Setup(embed.FS{}, nil, nil, hub)
	var handlerRan atomic.Bool
	r.GET("/api/shutdown-test", func(c *gin.Context) {
		handlerRan.Store(true)
		c.String(http.StatusOK, "served")
	})

	ts := httptest.NewServer(r)
	defer ts.Close()
	app := &App{Server: ts.Config}
	client := &http.Client{Timeout: 5 * time.Second}

	streamResp, err := client.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatalf("open events stream: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("events stream status = %d, want 200", streamResp.StatusCode)
	}

	app.BeginShutdown()

	streamDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, streamResp.Body)
		streamDone <- err
	}()
	select {
	case err := <-streamDone:
		if err != nil {
			t.Fatalf("read events stream after shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("events stream did not finish after App.BeginShutdown")
	}

	resp, err := client.Get(ts.URL + "/api/shutdown-test")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("request got status %d after App.BeginShutdown, want connection error", resp.StatusCode)
	}
	if handlerRan.Load() {
		t.Fatal("router handler ran after App.BeginShutdown")
	}

	if !middleware.IsStopping() {
		t.Fatal("middleware did not enter shutdown after App.BeginShutdown")
	}
}

// TestShutdownWaitsForInflight guards the drain semantics: Shutdown must
// block while a request is in flight and return only after it finishes —
// a regression to a short deadline or to Close() (which would cut the
// request) fails here. (The absence of any deadline is enforced by
// Shutdown using context.Background() — no WithTimeout in server.go.)
func TestShutdownWaitsForInflight(t *testing.T) {
	inFlight := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(inFlight)
		<-release
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	defer srv.Close()

	// Start a request and wait until its handler is actually running.
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()
	select {
	case <-inFlight:
	case <-time.After(time.Second):
		t.Fatal("handler never started")
	}

	app := &App{Server: srv}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.Shutdown() }()

	// Shutdown must NOT return while the request is still in flight.
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned while a request was in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not return after the in-flight request finished")
	}

	select {
	case resp := <-respCh:
		resp.Body.Close()
	case err := <-errCh:
		t.Fatalf("request failed: %v", err)
	case <-time.After(time.Second):
		t.Fatal("in-flight request did not complete")
	}
}

// TestShutdownNoServerIsNoop: Shutdown on an app that never started must
// not panic or block.
func TestShutdownNoServerIsNoop(t *testing.T) {
	app := &App{}
	if err := app.Shutdown(); err != nil {
		t.Fatalf("Shutdown without server returned error: %v", err)
	}
}
