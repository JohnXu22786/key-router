//go:build windows

package main

import (
	"log"
	"sync"

	"github.com/getlantern/systray"
)

// trayState holds the tray lifecycle for the Windows build.
type trayState struct {
	mu       sync.Mutex
	ctx      *subclassCtx // close-to-tray handler
	started  bool
	quitChan chan struct{} // closed when the user picks "Exit"
}

var tray = &trayState{quitChan: make(chan struct{})}

// StartTray runs the systray loop in a background goroutine and installs the
// close-to-tray window handler. hwnd is the webview window handle.
// Returns a channel that closes when the user chooses Exit.
func StartTray(hwnd uintptr) <-chan struct{} {
	tray.mu.Lock()
	if tray.started {
		tray.mu.Unlock()
		return tray.quitChan
	}
	tray.started = true
	tray.mu.Unlock()

	// Install the close handler BEFORE the webview message loop runs so the
	// first WM_CLOSE is intercepted. The webview window exists by now.
	tray.ctx = installTrayCloseHandler(hwnd, func() {
		log.Println("[tray] app hidden to tray")
	})

	go func() {
		systray.Run(onTrayReady, onTrayExit)
	}()
	return tray.quitChan
}

// onTrayReady populates the tray menu.
func onTrayReady() {
	systray.SetTitle("KeyRouter")
	systray.SetTooltip("KeyRouter — local AI API gateway")
	showItem := systray.AddMenuItem("Show KeyRouter", "Restore the main window")
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Exit", "Quit KeyRouter")

	go func() {
		for {
			select {
			case <-showItem.ClickedCh:
				log.Println("[tray] restore window")
				tray.mu.Lock()
				ctx := tray.ctx
				tray.mu.Unlock()
				if ctx != nil {
					showTrayWindow(ctx)
				}
			case <-quitItem.ClickedCh:
				log.Println("[tray] exit requested")
				tray.mu.Lock()
				ctx := tray.ctx
				tray.mu.Unlock()
				if ctx != nil {
					requestTrayQuit(ctx) // posts WM_CLOSE → window really closes
				}
				close(tray.quitChan)
				return
			}
		}
	}()
}

func onTrayExit() {
	log.Println("[tray] systray exited")
}
