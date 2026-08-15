package update

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// ScheduleRelaunchAfterExit starts a detached helper that waits for THIS
// process (by PID) to exit, then starts a fresh instance of the current
// executable. It powers the gateway restart endpoint (/api/restart): the
// old process must be gone before the new one binds the server port, so
// the new instance is never started here — only the helper is. Same
// wait-for-exit mechanism as the portable-update swap helper, without any
// file swapping.
func ScheduleRelaunchAfterExit() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate running executable: %w", err)
	}
	if runtime.GOOS == "windows" {
		return launchWindowsRelaunch(exe)
	}
	return launchPosixRelaunch(exe)
}

// launchWindowsRelaunch writes a small batch that waits for this process
// (by PID) to exit, starts a fresh copy of the exe, and deletes itself.
// The script gets a unique name so a retry can't splice new content into a
// still-running helper from a previous attempt (cmd re-reads batch files
// line by line).
func launchWindowsRelaunch(exe string) error {
	f, err := os.CreateTemp(os.TempDir(), "keyrouter-restart-*.bat")
	if err != nil {
		return fmt.Errorf("failed to create restart script: %w", err)
	}
	script := f.Name()
	content := windowsRelaunchScript(exe, os.Getpid())
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(script)
		return fmt.Errorf("failed to write restart script: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(script)
		return fmt.Errorf("failed to write restart script: %w", err)
	}
	cmd := exec.Command("cmd", "/c", script)
	cmd.SysProcAttr = hideWindowAttr()
	if err := cmd.Start(); err != nil {
		os.Remove(script)
		return fmt.Errorf("failed to launch restart script: %w", err)
	}
	return nil
}

// windowsRelaunchScript returns the .bat content that waits for THIS
// process (by PID) to exit, starts a fresh instance of the exe, and
// deletes itself. The wait gives up after ~5 minutes (PID reuse) and
// proceeds anyway — by then the old process is gone either way. No
// parenthesized blocks are used, so no delayed expansion is needed and
// paths containing "!" stay intact.
func windowsRelaunchScript(exePath string, pid int) string {
	return fmt.Sprintf(`@echo off
set /a n=0
:wait
tasklist /FI "PID eq %[1]d" 2>nul | find /I "%[1]d" >nul
if errorlevel 1 goto proceed
set /a n+=1
if %%n%% GEQ 300 goto proceed
ping -n 2 127.0.0.1 >nul
goto wait
:proceed
start "" "%[2]s"
del "%%~f0"
`, pid, exePath)
}
