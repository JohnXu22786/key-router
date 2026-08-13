//go:build windows

package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestShellExecuteOpenVerb verifies the ShellExecuteExW plumbing (struct
// layout, call convention): the call must actually start the target. Uses
// the "open" verb on cmd.exe with a marker-file side effect so no UAC
// prompt appears (the "runas" verb is the real updater path and cannot be
// exercised in tests without an elevation prompt).
func TestShellExecuteOpenVerb(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker.txt")
	args := `/c echo launched > "` + marker + `"`
	if err := shellExecute("open", `C:\Windows\System32\cmd.exe`, args); err != nil {
		t.Fatalf("shellExecute(open) failed: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return // target ran
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("shellExecute(open) reported success but the target never ran")
}

// TestShellExecuteErrorMapping: a missing target must surface as an error,
// not a silent success — the updater must never claim the installer was
// launched when nothing started.
func TestShellExecuteErrorMapping(t *testing.T) {
	if err := shellExecute("open", `C:\Windows\System32\no-such-zzz.exe`, ""); err == nil {
		t.Fatal("expected an error for a missing target, got nil")
	}
}
