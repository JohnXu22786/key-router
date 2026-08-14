package server

import (
	"net"
	"net/http"
	"testing"
	"time"
)

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
