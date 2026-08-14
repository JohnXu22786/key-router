//go:build windows

package main

import "log"

// setRestartQuitFn is a no-op on Windows: the graceful exit runs through
// the window-close path (requestRestartQuit posts WM_CLOSE → the webview
// loop returns → main drains in-flight requests and exits).
func setRestartQuitFn(func()) {}

// requestRestartQuit triggers the graceful shutdown without confirmation —
// the same path as the post-update exit (tray icon removed, real window
// close), so in-flight API calls finish before the process exits.
func requestRestartQuit() {
	log.Println("[main] exiting for restart")
	requestExitNow()
}
