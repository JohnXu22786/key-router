package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"

	"local-router/db"
	"local-router/health"
	"local-router/router"
	"local-router/selector"
	"local-router/server"

	"github.com/webview/webview_go"
)

// FreeConsole detaches the process from its console window (GUI mode only)
var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var freeConsole = kernel32.NewProc("FreeConsole")

//go:embed web/dist/*
var staticFS embed.FS

func main() {
	// Detach from console window immediately (GUI mode)
	freeConsole.Call()

	// Determine data directory
	dataDir := os.Getenv("LOCALROUTER_DATA")
	if dataDir == "" {
		execPath, err := os.Executable()
		if err != nil {
			dataDir = "./data"
		} else {
			dataDir = filepath.Join(filepath.Dir(execPath), "data")
		}
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("[main] cannot create data directory: %v", err)
	}

	// Set up log file (GUI mode has no console)
	logPath := filepath.Join(dataDir, "local-router.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err == nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Printf("[main] LocalRouter starting... dataDir=%s", dataDir)

	// Initialize database
	if err := db.Init(dataDir); err != nil {
		log.Fatalf("[main] database initialization failed: %v", err)
	}
	log.Println("[main] database initialized")

	// Initialize routing engine
	engine := selector.NewEngine()
	log.Println("[main] routing engine initialized")

	// Initialize and start health checker
	checker := health.NewChecker()
	checker.SetOnKeyRecovered(func(keyID int64) {
		engine.MarkKeyActive(keyID)
	})
	checker.Start()
	log.Println("[main] health checker started")

	// Setup HTTP router
	r := router.Setup(staticFS, engine, checker)

	// Start HTTP server in background
	app := server.New(r)
	if err := app.StartBackground(); err != nil {
		log.Fatalf("[main] server error: %v", err)
	}

	// Get port for webview URL
	port := app.GetPort()
	url := fmt.Sprintf("http://localhost:%d", port)

	log.Printf("[main] opening desktop window: %s", url)

	// Create desktop window with WebView2
	w := webview.New(false) // false = no devtools
	defer w.Destroy()
	w.SetTitle(fmt.Sprintf("LocalRouter v0.1.0 — %s", url))
	w.SetSize(900, 580, webview.HintNone)
	w.Navigate(url)
	w.Run()

	// When window closes, stop server
	log.Println("[main] window closed, shutting down...")
	app.Shutdown()
	log.Println("[main] LocalRouter stopped")
}
