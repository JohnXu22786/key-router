package update

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWindowsRelaunchScript guards the gateway-restart batch: it must wait
// for THIS process (by PID) to exit — a fresh instance would fail to bind
// the server port while the old one still lives — then start a fresh copy
// of the exe and delete itself. The capped wait must never fall through to
// start a second instance against a still-running process.
func TestWindowsRelaunchScript(t *testing.T) {
	s := windowsRelaunchScript(`D:\apps\KeyRouter\KeyRouter.exe`, 4242)
	for _, want := range []string{
		`PID eq 4242`,
		`start "" "D:\apps\KeyRouter\KeyRouter.exe"`,
		`del "%~f0"`,
		// The 300-iteration cap goes to a FINAL PID re-check, not straight to
		// the start: relaunching while the old process lives would spawn a
		// second instance fighting for the server port.
		`if %n% GEQ 300 goto finalcheck`,
		`:finalcheck`,
		`if errorlevel 1 goto proceed`,
		// Still alive after the final check → abort, never start a duplicate.
		`del "%~f0" & exit /b 1`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("relaunch script missing %q\n---\n%s", want, s)
		}
	}
	// Ordering: the final PID re-check MUST precede the start — the relaunch
	// script has no move guard, so the finalcheck/abort is its only protection
	// against racing a still-running process (a `:finalcheck` after `:proceed`
	// would be dead code and silently re-open the second-instance bug). The
	// standalone abort line must sit between them.
	proceedAt := strings.Index(s, ":proceed")
	finalcheckAt := strings.Index(s, ":finalcheck")
	if proceedAt < 0 || finalcheckAt < 0 || finalcheckAt > proceedAt {
		t.Errorf("relaunch script ordering wrong: :finalcheck (%d) must come before :proceed (%d)\n---\n%s", finalcheckAt, proceedAt, s)
	} else {
		abortAt := strings.Index(s, `del "%~f0" & exit /b 1`)
		if abortAt < 0 || abortAt < finalcheckAt || abortAt > proceedAt {
			t.Errorf("still-alive abort must sit between :finalcheck and :proceed\n---\n%s", s)
		}
	}
}

// TestPosixRelaunchScript guards the POSIX restart script: it must wait
// for THIS process (by PID) to exit, bounded so a recycled PID cannot
// stall the relaunch forever, then exec a fresh copy of the exe. The exe
// path must stay literal — a path containing a single quote must not break
// the script.
func TestPosixRelaunchScript(t *testing.T) {
	s := posixRelaunchScript(4242, `/opt/KeyRouter/app`)
	for _, want := range []string{
		`kill -0 4242`,
		`$n -lt 300`,
		`if kill -0 4242 2>/dev/null; then exit 1; fi`,
		`exec '/opt/KeyRouter/app'`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("posix relaunch script missing %q\n---\n%s", want, s)
		}
	}
	loopEnd := strings.Index(s, "done;")
	finalCheck := strings.Index(s, `if kill -0 4242 2>/dev/null; then exit 1; fi`)
	execAt := strings.Index(s, `exec '/opt/KeyRouter/app'`)
	if loopEnd < 0 || finalCheck <= loopEnd || execAt <= finalCheck {
		t.Errorf("final PID check must follow the wait loop and precede exec: loop=%d check=%d exec=%d\n---\n%s", loopEnd, finalCheck, execAt, s)
	}

	s2 := posixRelaunchScript(1, `/a'b`)
	if !strings.Contains(s2, `exec '/a'\''b'`) {
		t.Errorf("posix relaunch script does not escape single quotes\n---\n%s", s2)
	}
}

func TestPosixRelaunchScriptAbortsWhenPIDRemainsAlive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell behavior")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("POSIX shell unavailable: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "launched")
	next := writeRelaunchMarkerExecutable(t)
	prefix := "kill() { return 0; }; sleep() { :; }; "
	cmd := exec.Command(sh, "-c", prefix+posixRelaunchScript(4242, next))
	cmd.Env = append(os.Environ(), "MARKER="+marker)
	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("alive process should abort with status 1, got err %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("relaunch target ran despite the original PID remaining alive (stat err %v)", err)
	}
}

func TestPosixRelaunchScriptExecutesAfterPIDExits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell behavior")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("POSIX shell unavailable: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "launched")
	next := writeRelaunchMarkerExecutable(t)
	// The first liveness check succeeds; the second and final checks report
	// that the old process exited. The relaunch target must then run.
	prefix := `calls=0; kill() { calls=$((calls+1)); [ "$calls" -eq 1 ]; }; sleep() { :; }; `
	cmd := exec.Command(sh, "-c", prefix+posixRelaunchScript(4242, next))
	cmd.Env = append(os.Environ(), "MARKER="+marker)
	if err := cmd.Run(); err != nil {
		t.Fatalf("relaunch script failed after PID exit: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("relaunch target was not executed after PID exit: %v", err)
	}
}

func writeRelaunchMarkerExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "next")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n: > \"$MARKER\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
