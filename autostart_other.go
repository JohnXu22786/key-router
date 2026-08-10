//go:build !windows

package main

import "errors"

// Autostart (launch at login) is Windows-only for now. macOS could use a
// LaunchAgent and Linux an XDG autostart .desktop entry — not implemented.
func setAutostartEnabled(enabled bool) error {
	return errors.New("autostart is only supported on Windows")
}

func autostartEnabled() bool {
	return false
}
