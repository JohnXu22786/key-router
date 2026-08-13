package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"key-router/db"
	"key-router/handler"
	"key-router/health"
	"key-router/middleware"
	"key-router/model"
	"key-router/router"
	"key-router/selector"
	"key-router/server"
	"key-router/update"

	"github.com/webview/webview_go"
)

// version is the app version, overridable at build time:
//
//	go build -ldflags "-X main.version=1.2.3"
var version = "0.1.0"

//go:embed web/dist/*
var staticFS embed.FS

// web/dist is generated build output (gitignored, never committed — hashed
// asset filenames made every frontend change collide in merges). Build it
// first: `cd web && npm run build`, or `go build` fails on this embed.

func main() {
	// Detach from console window (GUI mode; Windows-only, see platform files)
	detachConsole()

	// Determine data directory. User data always lives in the system
	// application-data directory — never next to the executable — so it
	// survives updates across every build type and platform. KEYROUTER_DATA
	// overrides it (used for testing / isolated instances).
	dataDir := resolveDataDir(os.Getenv, defaultDataDir)
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Printf("[main] cannot create data directory: %v", err)
		showFatalError(fmt.Sprintf("KeyRouter failed to start:\n\nCannot create data directory:\n%v", err))
		os.Exit(1)
	}

	// Set up log file (GUI mode has no console)
	logPath := filepath.Join(dataDir, "key-router.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err == nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Printf("[main] KeyRouter starting... dataDir=%s", dataDir)

	// One-time migration from legacy data locations (exe-adjacent ./data and
	// the v0.1.x LocalRouter app-data dir). Runs after the log is set up so
	// migration messages are visible.
	migrateLegacyData(dataDir, legacyDataDirs(runtime.GOOS, os.Getenv, os.UserHomeDir))

	// Initialize database
	if err := db.Init(dataDir); err != nil {
		log.Printf("[main] database initialization failed: %v", err)
		showFatalError(fmt.Sprintf("KeyRouter failed to start:\n\nDatabase initialization failed:\n%v", err))
		os.Exit(1)
	}
	log.Println("[main] database initialized")

	// Initialize routing engine
	engine := selector.NewEngine()

	// Restore persisted rate-limit windows so long-window budgets (daily,
	// weekly, monthly) survive restarts
	windowsPath := filepath.Join(dataDir, "windows.json")
	if err := engine.WindowManager.LoadFromFile(windowsPath); err != nil {
		log.Printf("[main] failed to restore window state (continuing fresh): %v", err)
	}

	// Prune window state of keys deleted while the app was closed (otherwise
	// windows.json grows forever with key churn). Only when the key list
	// loads — an empty list on a DB error must NOT wipe all windows.
	var keyIDs []int64
	if err := db.GetDB().Model(&model.Key{}).Pluck("id", &keyIDs).Error; err != nil {
		log.Printf("[main] failed to load key IDs for window pruning: %v", err)
	} else {
		known := make(map[int64]bool, len(keyIDs))
		for _, id := range keyIDs {
			known[id] = true
		}
		engine.WindowManager.Prune(known)
	}

	// pruneWindows removes window state for keys that no longer exist (e.g.
	// deleted mid-session while an in-flight relay re-created them)
	pruneWindows := func() {
		var ids []int64
		if err := db.GetDB().Model(&model.Key{}).Pluck("id", &ids).Error; err != nil {
			log.Printf("[main] window prune key-list error: %v", err)
			return
		}
		known := make(map[int64]bool, len(ids))
		for _, id := range ids {
			known[id] = true
		}
		engine.WindowManager.Prune(known)
	}

	// Periodically persist rate-limit windows (also saved on shutdown).
	// persistDone lets shutdown join the goroutine so the final save can't
	// race an in-flight ticker save on the same temp file.
	stopPersist := make(chan struct{})
	persistDone := make(chan struct{})
	go func() {
		defer close(persistDone)
		// Persist window counters frequently so a crash or a forced kill loses
		// at most the last few seconds of rate-limit usage, not the whole day.
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				pruneWindows()
				if err := engine.WindowManager.SaveToFile(windowsPath); err != nil {
					log.Printf("[main] failed to persist window state: %v", err)
				}
			case <-stopPersist:
				return
			}
		}
	}()

	log.Println("[main] routing engine initialized")

	// Initialize and start health checker
	checker := health.NewChecker()
	checker.SetOnKeyRecovered(func(keyID int64) {
		engine.MarkKeyActive(keyID)
	})
	checker.SetOnKeyFailed(func(keyID int64, reason string) {
		engine.MarkKeyDisabled(keyID, reason)
	})
	checker.Start()
	log.Println("[main] health checker started")

	// Setup HTTP router
	r := router.Setup(staticFS, engine, checker)
	// Launch-at-login (Windows only; injected after the handler is built so
	// the router's internal handler sees the functions).
	router.SetAutostartHooks(autostartEnabled, setAutostartEnabled)

	// Auto-update check: on startup and then daily. Detects the install mode
	// (portable by default; installed only when the NSIS marker file exists)
	// and stores the result for the UI notification — never applies
	// automatically.
	router.SetAutoCheckCallback(func(h *handler.AdminHandler) {
		updater := update.NewClient(version)
		updater.AutoCheck(h.SetAutoCheckInfo)
	})

	// After an update is applied the process must exit so the new binary can
	// replace it (portable swap script / installed installer). The handler
	// calls the hook after responding to the UI.
	router.SetUpdateExitHook(requestExitForUpdate)

	// Remove leftover updater temp files (interrupted downloads, cancelled
	// installers) from previous runs. Best-effort — never fails startup.
	update.CleanupStaleDownloads()

	// Start HTTP server in background
	app := server.New(r)
	if err := app.StartBackground(); err != nil {
		// The console was detached above (GUI mode) — log.Fatalf alone would
		// be invisible. Show a message box so a double-click launch isn't a
		// silent failure (e.g. port already in use by another instance).
		log.Printf("[main] server error: %v", err)
		showFatalError(fmt.Sprintf("KeyRouter failed to start:\n\n%v\n\nCheck the log file for details.", err))
		os.Exit(1)
	}

	// Get port for webview URL
	port := app.GetPort()
	url := fmt.Sprintf("http://localhost:%d", port)

	log.Printf("[main] opening desktop window: %s", url)

	// Create desktop window with WebView2
	w := webview.New(false) // false = no devtools
	defer w.Destroy()
	w.SetTitle(fmt.Sprintf("KeyRouter v%s — %s", version, url))
	w.SetSize(900, 580, webview.HintNone)
	w.Navigate(url)

	// Bind the browser-opening helper for the UI's external links (project
	// homepage, license, etc.). The webview library has no new-window
	// handling, so target=_blank links would open a bare WebView2 popup
	// inside the app — the UI calls this binding instead (see openurl.go).
	if err := w.Bind("openExternal", openExternal); err != nil {
		log.Printf("[main] failed to bind openExternal: %v", err)
	}

	// Apply the app icon to the window itself (title bar + taskbar button).
	// The webview library only sets the generic IDI_APPLICATION class icon,
	// so this must be done explicitly via WM_SETICON (no-op on other OSes).
	setWindowIcon(uintptr(w.Window()))
	// Record the window handle for the post-update exit path (closes the
	// window when no close-to-tray handler is installed).
	setUpdateExitWindow(uintptr(w.Window()))

	// System tray (Windows): clicking the window X hides to the tray instead
	// of quitting; single-clicking the tray icon restores the window, the
	// right-click menu restores it or exits for real. On other platforms
	// StartTray is a no-op and closing quits as before.
	trayQuit := StartTray(uintptr(w.Window()))

	// Run the message loop. With the tray active, Run() returns only when
	// the user picks "Exit" from the tray menu (WM_CLOSE is intercepted and
	// the window is hidden instead of destroyed).
	w.Run()
	_ = trayQuit

	// When window closes, stop server
	log.Println("[main] window closed, shutting down...")
	// Reject NEW requests first (in-flight SSE streams are allowed to finish
	// in the background — the agent keeps receiving its response until it
	// completes, then the process exits).
	middleware.BeginShutdown()
	close(stopPersist)
	<-persistDone
	// Disable (not just Stop) so an in-flight async Restart from
	// UpdateSettings can't relaunch the loop after shutdown
	checker.Disable()
	// Stop serving FIRST so no in-flight relay can increment windows between
	// the save and shutdown (those increments would be lost on restart)
	app.Shutdown()
	pruneWindows()
	if err := engine.WindowManager.SaveToFile(windowsPath); err != nil {
		log.Printf("[main] failed to persist window state on shutdown: %v", err)
	}
	log.Println("[main] KeyRouter stopped")
}
