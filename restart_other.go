//go:build !windows

package main

import (
	"log"
	"os"
	"sync"
	"time"
)

// restartQuit holds the webview terminate callback used by
// requestRestartQuit. The mutex is needed because setRestartQuitFn runs on
// the main goroutine after the window exists while requestRestartQuit can
// run on an HTTP goroutine as soon as the server is up — a restart request
// in that brief startup window must not race the write.
var restartQuit = struct {
	sync.Mutex
	fn func()
}{}

// setRestartQuitFn records the webview terminate callback (a no-op on
// Windows, where the tray's WM_CLOSE path is used instead). webview's
// Terminate is safe to call from any goroutine.
func setRestartQuitFn(fn func()) {
	restartQuit.Lock()
	restartQuit.fn = fn
	restartQuit.Unlock()
}

// requestRestartQuit ends the webview loop; main's shutdown path then
// rejects new requests, drains in-flight API calls, persists state, and
// exits — the wait-for-exit helper starts the fresh instance. If the quit
// path was never recorded (restart request before the window existed),
// fall back to a delayed hard exit like the post-update exit: the process
// must still go away or the helper waits forever. The fallback skips the
// in-flight drain, but it can only trigger in the startup window before
// any request is realistically in flight.
func requestRestartQuit() {
	restartQuit.Lock()
	fn := restartQuit.fn
	restartQuit.Unlock()
	if fn == nil {
		log.Println("[main] no webview quit path recorded — hard-exiting for restart")
		go func() {
			time.Sleep(1 * time.Second)
			os.Exit(0)
		}()
		return
	}
	log.Println("[main] terminating webview loop for restart")
	fn()
}
