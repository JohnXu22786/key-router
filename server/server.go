package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"local-router/db"
	"local-router/model"

	"github.com/gin-gonic/gin"
)

// App holds all application components
type App struct {
	Router *gin.Engine
	Server *http.Server
	port   int
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

// StartBackground starts the HTTP server in a background goroutine
func (a *App) StartBackground() error {
	// Get port from settings
	portStr := db.GetSetting(model.SettingPort)
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		port = 9998
	}
	a.port = port

	addr := fmt.Sprintf(":%d", port)
	a.Server = &http.Server{
		Addr:         addr,
		Handler:      a.Router,
		ReadTimeout:  300 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Printf("[server] listening on http://localhost%s", addr)
		log.Printf("[server] forwarding API: http://localhost%s/v1/chat/completions", addr)

		if err := a.Server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[server] error: %v", err)
		}
	}()

	return nil
}

// Shutdown gracefully stops the HTTP server
func (a *App) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.Server.Shutdown(ctx)
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
