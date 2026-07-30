package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"local-router/db"
	"local-router/model"

	"github.com/gin-gonic/gin"
)

// App holds all application components
type App struct {
	Router *gin.Engine
	Server *http.Server
}

// New creates a new server instance
func New(router *gin.Engine) *App {
	return &App{
		Router: router,
	}
}

// Start starts the HTTP server and auto-opens browser
func (a *App) Start() error {
	// Get port from settings
	portStr := db.GetSetting(model.SettingPort)
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		port = 9998
	}

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
		log.Printf("[server] web UI: http://localhost%s", addr)
		log.Printf("[server] management API: http://localhost%s/api/health", addr)

		if err := a.Server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[server] failed to start: %v", err)
		}
	}()

	// Try to auto-open browser
	openBrowser(fmt.Sprintf("http://localhost%s", addr))

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[server] shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.Server.Shutdown(ctx)
}

// openBrowser attempts to open the default browser
func openBrowser(url string) {
	// Windows: use cmd.exe /c start
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
