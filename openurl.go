package main

import (
	"log"
	"os/exec"
	"runtime"
)

// openExternal opens a URL in the user's default browser. It is bound to
// the webview as "openExternal" and called by the UI for external links —
// the embedded webview has no new-window handling, so a plain
// target=_blank link would open a bare WebView2 popup inside the app
// instead of the system browser.
func openExternal(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 url.dll,FileProtocolHandler opens the URL with the
		// default browser without flashing a cmd.exe console.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[main] failed to open %q in browser: %v", url, err)
		return
	}
	// Reap the child in the background: Wait() frees the process handle
	// (otherwise leaked until the parent exits) and prevents zombies on
	// Unix. The browser helper exits on its own quickly.
	go cmd.Wait()
}
