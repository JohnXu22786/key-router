package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"key-router/db"
	"key-router/model"

	"github.com/gin-gonic/gin"
)

// App holds all application components
type App struct {
	Router *gin.Engine
	Server *http.Server
	port   int
	mu     sync.Mutex
	stop   bool // set by BeginShutdown: reject new requests, finish in-flight
}

// RejectingNew reports whether the server is shutting down (new requests
// should be refused so in-flight SSE streams can finish in the background).
func (a *App) RejectingNew() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stop
}

// BeginShutdown marks the server as shutting down: new requests (relay and
// admin) return 503 while currently-streaming responses are allowed to
// finish, then Shutdown waits for them.
func (a *App) BeginShutdown() {
	a.mu.Lock()
	a.stop = true
	a.mu.Unlock()
}

// New creates a new server instance
func New(router *gin.Engine) *App {
	return &App{
		Router: router,
	}
}

// GetPort returns the server port
func (a *App) GetPort() int {
	return a.port
}

// StartBackground starts the HTTP server in a background goroutine.
// Returns an error if the port is invalid or the listener cannot bind
// (e.g. port already in use).
func (a *App) StartBackground() error {
	portStr := db.GetSetting(model.SettingPort)
	port, err := strconv.Atoi(portStr)
	// A persisted value outside the valid range (hand-edited DB) must not
	// brick startup — fall back to the default instead of failing to bind.
	if err != nil || port <= 0 || port > 65535 {
		port = 9998
	}
	a.port = port

	// Bind to localhost only: the management API is unauthenticated and
	// returns API keys in plaintext, so it must never be exposed on the LAN.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot bind %s: %w", addr, err)
	}

	a.Server = &http.Server{
		Addr:    addr,
		Handler: a.Router,
		// ReadTimeout guards slow/hung clients; WriteTimeout must stay 0:
		// net/http applies it as a single deadline for the whole response,
		// which would kill long SSE streams mid-stream.
		ReadTimeout: 300 * time.Second,
		IdleTimeout: 60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Printf("[server] listening on http://localhost:%d", port)
		log.Printf("[server] forwarding API: http://localhost:%d/v1/chat/completions (+ /v1/responses, /v1/messages, /v1/embeddings)", port)

		if err := a.Server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[server] failed to serve: %v", err)
		}
	}()

	return nil
}

// Shutdown gracefully stops the HTTP server: rejects new connections (via
// net/http Shutdown) and waits for in-flight SSE streams to complete before
// returning. BeginShutdown should be called first so application-level
// requests are also refused while streams finish.
func (a *App) Shutdown() error {
	a.BeginShutdown()
	if a.Server == nil {
		return nil
	}
	// Grace period long enough for typical agent streams; Shutdown keeps
	// serving active connections until they finish or this expires.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := a.Server.Shutdown(ctx)
	if err != nil {
		// e.g. in-flight SSE streams longer than the grace period
		log.Printf("[server] shutdown incomplete: %v", err)
	}
	return err
}

// Start starts the HTTP server (blocking, with auto-open browser - deprecated, use StartBackground)
func (a *App) Start() error {
	if err := a.StartBackground(); err != nil {
		return err
	}

	// Try to auto-open browser
	openBrowser(fmt.Sprintf("http://localhost:%d", a.port))

	// Wait forever
	select {}
}

// openBrowser attempts to open the default browser (Windows only)
func openBrowser(url string) {
	attr := &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}
	proc, err := os.StartProcess("cmd.exe", []string{"cmd.exe", "/c", "start", url}, attr)
	if err == nil {
		proc.Release()
		return
	}
	log.Printf("[server] could not auto-open browser (error: %v), navigate to %s", err, url)
}
