//go:build !windows

package update

import "syscall"

// runAsElevated is a no-op on non-Windows platforms (applyInstalled already
// rejects non-Windows before reaching here).
func runAsElevated(path, args string) error {
	return nil
}

// hideWindowAttr is a no-op on non-Windows platforms (the batch helpers are
// Windows-only).
func hideWindowAttr() *syscall.SysProcAttr {
	return nil
}
