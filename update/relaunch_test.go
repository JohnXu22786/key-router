package update

import (
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
		`exec '/opt/KeyRouter/app'`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("posix relaunch script missing %q\n---\n%s", want, s)
		}
	}
	s2 := posixRelaunchScript(1, `/a'b`)
	if !strings.Contains(s2, `exec '/a'\''b'`) {
		t.Errorf("posix relaunch script does not escape single quotes\n---\n%s", s2)
	}
}
