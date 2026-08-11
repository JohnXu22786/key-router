//go:build !windows

package update

// runAsElevated is a no-op on non-Windows platforms (applyInstalled already
// rejects non-Windows before reaching here).
func runAsElevated(path, args string) error {
	return nil
}
