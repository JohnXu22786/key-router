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
