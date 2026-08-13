//go:build !windows

package main

import "log"

// detachConsole is a no-op on non-Windows platforms
func detachConsole() {}

// showFatalError prints the error to stderr on non-Windows platforms (a
// console is present there)
func showFatalError(msg string) {
	log.Printf("FATAL: %s", msg)
}

// setWindowIcon is a no-op on non-Windows platforms (each platform's window
// shows the icon from its own packaging: .desktop/icns/etc.)
func setWindowIcon(hwnd uintptr) {}
