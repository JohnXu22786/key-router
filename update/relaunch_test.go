package update

import (
	"strings"
	"testing"
)

// TestWindowsRelaunchScript guards the gateway-restart batch: it must wait
// for THIS process (by PID) to exit — a fresh instance would fail to bind
// the server port while the old one still lives — then start a fresh copy
// of the exe and delete itself.
func TestWindowsRelaunchScript(t *testing.T) {
	s := windowsRelaunchScript(`D:\apps\KeyRouter\KeyRouter.exe`, 4242)
	for _, want := range []string{
		`PID eq 4242`,
		`GEQ 300`,
		`ping -n 2`,
		`start "" "D:\apps\KeyRouter\KeyRouter.exe"`,
		`del "%~f0"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("relaunch script missing %q\n---\n%s", want, s)
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
