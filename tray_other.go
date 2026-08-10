//go:build !windows

package main

// trayState is a no-op on non-Windows platforms (no system tray integration;
// closing the window quits the app as before).
type trayState struct{}

var tray = &trayState{}

// StartTray is a no-op on non-Windows platforms. It returns a channel that
// never closes (the app quits when the window closes, as before).
func StartTray(hwnd uintptr) <-chan struct{} {
	return make(chan struct{})
}
