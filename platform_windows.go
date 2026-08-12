//go:build windows

package main

import (
	"log"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

// FreeConsole detaches the process from its console window (GUI mode only)
var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var freeConsole = kernel32.NewProc("FreeConsole")

// user32 for the fatal-error message box (the console is detached, so
// log.Fatalf alone would be invisible)
var user32 = syscall.NewLazyDLL("user32.dll")
var messageBoxW = user32.NewProc("MessageBoxW")
var showWindow = user32.NewProc("ShowWindow")
var setForegroundWindow = user32.NewProc("SetForegroundWindow")

// comctl32 for SetWindowSubclass (tray hide-on-close)
var comctl32 = syscall.NewLazyDLL("comctl32.dll")
var setWindowSubclass = comctl32.NewProc("SetWindowSubclass")
var defSubclassProc = comctl32.NewProc("DefSubclassProc")
var removeWindowSubclass = comctl32.NewProc("RemoveWindowSubclass")

// shell32 for ExtractIconExW (window icon extraction from the exe resource)
var shell32 = syscall.NewLazyDLL("shell32.dll")
var extractIconExW = shell32.NewProc("ExtractIconExW")

// Window message constants
const (
	wmClose    = 0x0010
	wmDestroy  = 0x0002
	wmSetIcon  = 0x0080
	iconSmall  = 0 // ICON_SMALL — title bar
	iconBig    = 1 // ICON_BIG   — taskbar / Alt-Tab
	swHide     = 0
	swShow     = 0x0005
)

// detachConsole detaches from the console window (GUI mode)
func detachConsole() {
	freeConsole.Call()
}

// showFatalError displays a modal error dialog (GUI mode)
func showFatalError(msg string) {
	title, _ := syscall.UTF16PtrFromString("KeyRouter")
	text, _ := syscall.UTF16PtrFromString(msg)
	messageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10 /* MB_ICONERROR */)
}

// confirmExit shows a Yes/No confirmation before quitting from the tray.
// Returns true when the user confirms.
func confirmExit() bool {
	title, _ := syscall.UTF16PtrFromString("Exit KeyRouter")
	text, _ := syscall.UTF16PtrFromString("Are you sure you want to exit KeyRouter?\n\nIn-flight requests will be allowed to finish.")
	// MB_YESNO (0x4) | MB_ICONQUESTION (0x20) | MB_DEFBUTTON2 (0x100)
	ret, _, _ := messageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x4|0x20|0x100)
	return ret == 6 // IDYES
}

// subclassCtx holds the per-window subclass state.
type subclassCtx struct {
	hwnd   uintptr
	onHide func()
	mu     sync.Mutex
	quit   bool // set by tray "Exit": next WM_CLOSE really closes
}

// subclassRefs maps the uIdSubclass value back to the context. The subclass
// callback receives dwRefData as a plain uintptr; converting it back to a Go
// pointer trips vet's unsafe checks, so we keep the mapping in a table keyed
// by the subclass id instead (the window lives as long as the app, so this
// never leaks).
var (
	subclassRefs   = make(map[uintptr]*subclassCtx)
	subclassRefsMu sync.Mutex
	nextSubclassID uintptr = 1
)

// traySubclassProc is the window subclass callback: WM_CLOSE hides the
// window instead of destroying it (tray mode), unless quit was requested.
func traySubclassProc(hwnd, msg, wParam, lParam, uIdSubclass, dwRefData uintptr) uintptr {
	subclassRefsMu.Lock()
	ctx := subclassRefs[uIdSubclass]
	subclassRefsMu.Unlock()
	if ctx == nil {
		ret, _, _ := defSubclassProc.Call(hwnd, msg, wParam, lParam, uIdSubclass, dwRefData)
		return ret
	}
	switch msg {
	case wmClose:
		ctx.mu.Lock()
		quit := ctx.quit
		ctx.mu.Unlock()
		if quit {
			// Let the window actually close (tray Exit was chosen).
			ret, _, _ := defSubclassProc.Call(hwnd, msg, wParam, lParam, uIdSubclass, dwRefData)
			return ret
		}
		log.Println("[tray] window close intercepted — hiding to tray")
		showWindow.Call(ctx.hwnd, swHide)
		if ctx.onHide != nil {
			ctx.onHide()
		}
		return 0 // swallow the close
	case wmDestroy:
		// Window is really going away (app quit): clean up the subclass and
		// release the window icons (nothing can query them anymore).
		removeWindowSubclass.Call(hwnd, syscall.NewCallback(traySubclassProc), dwRefData)
		if windowIconBig != 0 {
			destroyIcon.Call(windowIconBig)
			windowIconBig = 0
		}
		if windowIconSmall != 0 {
			destroyIcon.Call(windowIconSmall)
			windowIconSmall = 0
		}
	}
	ret, _, _ := defSubclassProc.Call(hwnd, msg, wParam, lParam, uIdSubclass, dwRefData)
	return ret
}

// installTrayCloseHandler subclasses the webview window so clicking the X
// hides it to the tray instead of closing the app. Returns a context usable
// by the tray menu (show / quit).
func installTrayCloseHandler(hwnd uintptr, onHide func()) *subclassCtx {
	ctx := &subclassCtx{hwnd: hwnd, onHide: onHide}
	cb := syscall.NewCallback(traySubclassProc)
	subclassRefsMu.Lock()
	id := nextSubclassID
	nextSubclassID++
	subclassRefs[id] = ctx
	subclassRefsMu.Unlock()
	setWindowSubclass.Call(hwnd, cb, id, 0)
	log.Printf("[tray] close-to-tray handler installed (hwnd=%d)", hwnd)
	return ctx
}

// showTrayWindow makes the hidden window visible and brings it to the front.
func showTrayWindow(ctx *subclassCtx) {
	showWindow.Call(ctx.hwnd, swShow)
	setForegroundWindow.Call(ctx.hwnd)
}

// requestTrayQuit marks the next WM_CLOSE as a real close. The caller then
// posts WM_CLOSE to the window so the app actually exits.
func requestTrayQuit(ctx *subclassCtx) {
	ctx.mu.Lock()
	ctx.quit = true
	ctx.mu.Unlock()
	postMessage.Call(ctx.hwnd, wmClose, 0, 0)
}

// postMessage posts a message to the window's message queue.
var postMessage = user32.NewProc("PostMessageW")

// sendMessageW sends a message to a window and waits for its processing.
var sendMessageW = user32.NewProc("SendMessageW")

// destroyIcon releases an HICON obtained from ExtractIconExW.
var destroyIcon = user32.NewProc("DestroyIcon")

// windowIconBig/Small hold the HICONs applied to the webview window. The
// window (and the shell, which queries it via WM_GETICON for the taskbar and
// Alt-Tab) retains these handles for as long as the window exists — WM_SETICON
// does NOT copy the icon. They are destroyed when the window is destroyed
// (see traySubclassProc's WM_DESTROY handling).
var (
	windowIconBig   uintptr
	windowIconSmall uintptr
)

// setWindowIcon applies the exe's embedded icon to the webview window so the
// taskbar button (ICON_BIG) and the title bar (ICON_SMALL) show the KeyRouter
// icon. The webview library registers its window class with the generic
// IDI_APPLICATION icon, so without WM_SETICON the window always shows the
// default blank icon even when the exe has an icon resource.
func setWindowIcon(hwnd uintptr) {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	pathPtr, err := syscall.UTF16PtrFromString(exePath)
	if err != nil {
		return
	}
	var big, small uintptr
	ret, _, _ := extractIconExW.Call(uintptr(unsafe.Pointer(pathPtr)), 0, uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1)
	if ret == 0 {
		log.Println("[window] no icon resource in exe; taskbar/title bar keep the default icon")
		return
	}
	if uint32(ret) == ^uint32(0) { // UINT_MAX: icon extraction failed
		log.Printf("[window] failed to extract the exe icon (ret=%d)", ret)
		return
	}
	if big != 0 {
		sendMessageW.Call(hwnd, wmSetIcon, iconBig, big)
		windowIconBig = big
	}
	if small != 0 {
		sendMessageW.Call(hwnd, wmSetIcon, iconSmall, small)
		windowIconSmall = small
	}
	log.Println("[window] window icon set (taskbar + title bar)")
}
