//go:build !windows

package main

import (
	"os"
	"time"
)

// trayState is a no-op on non-Windows platforms (no system tray integration;
// closing the window quits the app as before).
type trayState struct{}

var tray = &trayState{}

// StartTray is a no-op on non-Windows platforms. It returns a channel that
// never closes (the app quits when the window closes, as before).
func StartTray(hwnd uintptr) <-chan struct{} {
	return make(chan struct{})
}

// setUpdateExitWindow is a no-op on non-Windows platforms (the webview
// window handle is not needed — the post-update exit is a hard exit).
func setUpdateExitWindow(hwnd uintptr) {}

// requestExitForUpdate exits the process after an update has been applied.
// POSIX portable updates swap the exe and schedule the new binary via a
// waiting shell; this process must not keep running (it would hold the
// server port and keep executing the old binary). The exit is delayed one
// second so the HTTP response reaches the UI first (net/http flushes it
// when the handler returns; a synchronous os.Exit would lose it and the UI
// would report a failed update that actually succeeded).
func requestExitForUpdate() {
	go func() {
		time.Sleep(1 * time.Second)
		os.Exit(0)
	}()
}
