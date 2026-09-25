package main

import "testing"

func TestTrayStartupSkipsCloseHandlerAfterExitDuringSetup(t *testing.T) {
	var guard trayStartupGuard

	// Tray icon setup runs before the close handler is installed. A restart or
	// update can request exit and queue WM_CLOSE during that setup.
	if !guard.requestExit() {
		t.Fatal("first exit request was rejected")
	}

	installed := false
	if guard.installCloseHandler(func() { installed = true }) {
		t.Fatal("close handler installed after an exit request")
	}
	if installed {
		t.Fatal("close handler callback ran after an exit request")
	}
}
