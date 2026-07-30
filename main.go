package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"local-router/db"
	"local-router/health"
	"local-router/router"
	"local-router/selector"
	"local-router/server"
)

//go:embed web/dist/*
var staticFS embed.FS

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("[main] LocalRouter starting...")

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

	// Start server
	app := server.New(r)
	if err := app.Start(); err != nil {
		log.Fatalf("[main] server error: %v", err)
	}

	log.Println("[main] LocalRouter stopped")
}
