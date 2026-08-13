//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

// The Windows tray is implemented directly with Shell_NotifyIconW instead of
// getlantern/systray: a single click on the tray icon opens the main window
// right away, and the context menu appears only on right-click. The systray
// library cannot do this — it shows the menu for left clicks too and offers
// no hook to change that.
//
// The tray notification window is created on the calling (webview) thread,
// so the webview message loop drives it — same requirement as the previous
// systray setup.

// trayState holds the tray lifecycle for the Windows build.
type trayState struct {
	mu       sync.Mutex
	ctx      *subclassCtx // close-to-tray handler
	started  bool
	quitChan chan struct{} // closed when the user picks "Exit"

	exitRequested  bool            // Exit confirmed once; guards quitChan close
	win            uintptr         // hidden tray notification window
	nid            *notifyIconData // Shell_NotifyIcon registration (re-added on Explorer restart)
	taskbarCreated uintptr         // WM_TASKBARCREATED, registered at startup
}

var tray = &trayState{quitChan: make(chan struct{})}

// Win32 procs used only by the tray (DLL handles live in platform_windows.go).
var (
	pCreateWindowExW       = user32.NewProc("CreateWindowExW")
	pRegisterClassExW      = user32.NewProc("RegisterClassExW")
	pDefWindowProcW        = user32.NewProc("DefWindowProcW")
	pRegisterWindowMessage = user32.NewProc("RegisterWindowMessageW")
	pLoadIconW             = user32.NewProc("LoadIconW")
	pCreatePopupMenu       = user32.NewProc("CreatePopupMenu")
	pAppendMenuW           = user32.NewProc("AppendMenuW")
	pTrackPopupMenu        = user32.NewProc("TrackPopupMenu")
	pDestroyMenu           = user32.NewProc("DestroyMenu")
	pGetCursorPos          = user32.NewProc("GetCursorPos")
	pShellNotifyIcon       = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandle       = kernel32.NewProc("GetModuleHandleW")
)

// Window message and Shell_NotifyIcon constants.
const (
	wmApp          = 0x8000
	wmTrayCallback = wmApp + 1 // uCallbackMessage: tray icon events
	wmLButtonUp    = 0x0202
	wmRButtonUp    = 0x0205

	nimAdd    = 0x00000000
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
)

// Popup menu constants.
const (
	mfString       = 0x00000000
	mfSeparator    = 0x00000800
	tpmRightButton = 0x00000002
	tpmReturnCmd   = 0x00000100
)

// Tray menu command IDs (TrackPopupMenu return values).
const (
	menuShowKeyRouter = 1
	menuExit          = 2
)

// idiApplication is the standard application icon, used as a fallback when
// the exe's icon resource cannot be extracted.
const idiApplication = 32512

// wndClassExW mirrors WNDCLASSEXW.
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

// notifyIconData mirrors NOTIFYICONDATAW (same layout as the systray
// library's, which is known to work with Shell_NotifyIconW).
type notifyIconData struct {
	cbSize            uint32
	hWnd              uintptr
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	hIcon             uintptr
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uTimeoutOrVersion uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
	guidItem          syscall.GUID
	hBalloonIcon      uintptr
}

// StartTray registers the tray icon and, when that succeeds, installs the
// close-to-tray window handler. hwnd is the webview window handle. Returns a
// channel that closes when the user chooses Exit.
func StartTray(hwnd uintptr) <-chan struct{} {
	tray.mu.Lock()
	if tray.started {
		tray.mu.Unlock()
		return tray.quitChan
	}
	tray.started = true
	tray.mu.Unlock()

	// Create the hidden tray notification window and register the icon. Only
	// if that succeeds do we install the close-to-tray handler: hiding the
	// window on X without a tray icon to restore it would strand the app.
	if err := setupTrayIcon(); err != nil {
		log.Printf("[tray] tray setup failed (%v) — window X will close the app", err)
		return tray.quitChan
	}

	// Install the close handler BEFORE the webview message loop runs so the
	// first WM_CLOSE is intercepted. The webview window exists by now.
	tray.ctx = installTrayCloseHandler(hwnd, func() {
		log.Println("[tray] app hidden to tray")
	})
	return tray.quitChan
}

// setupTrayIcon registers the Explorer-restart message, creates the hidden
// notification window and adds the tray icon.
func setupTrayIcon() error {
	// Explorer sends this message after a restart; the icon must then be
	// re-added. Registered before the window exists so its wndproc can
	// handle the message from the start.
	tbPtr, err := syscall.UTF16PtrFromString("TaskbarCreated")
	if err != nil {
		return err
	}
	res, _, _ := pRegisterWindowMessage.Call(uintptr(unsafe.Pointer(tbPtr)))
	if res == 0 {
		return fmt.Errorf("RegisterWindowMessageW(TaskbarCreated) failed")
	}
	tray.taskbarCreated = uintptr(res)
	if err := createTrayWindow(); err != nil {
		return err
	}
	return registerTrayIcon()
}

// createTrayWindow creates the hidden window that receives Shell_NotifyIcon
// callback messages (click, right-click, Explorer restart). It is never
// shown; the webview message loop pumps its messages.
func createTrayWindow() error {
	const className = "KeyRouterTrayWindow"

	classPtr, err := syscall.UTF16PtrFromString(className)
	if err != nil {
		return err
	}
	inst, _, _ := pGetModuleHandle.Call(0)
	if inst == 0 {
		return fmt.Errorf("GetModuleHandleW failed")
	}
	wc := &wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   syscall.NewCallback(trayWndProc),
		hInstance:     inst,
		hbrBackground: 6, // COLOR_WINDOW + 1; never painted anyway
		lpszClassName: classPtr,
	}
	if ret, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(wc))); ret == 0 {
		return fmt.Errorf("RegisterClassExW failed: %v", err)
	}
	win, _, err := pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(classPtr)),
		0, // no title
		0, // WS_OVERLAPPED; never shown
		0, 0, 0, 0,
		0, 0, // no parent, no menu
		inst,
		0,
	)
	if win == 0 {
		return fmt.Errorf("CreateWindowExW failed: %v", err)
	}
	tray.win = win
	return nil
}

// registerTrayIcon adds the tray icon via Shell_NotifyIconW and remembers
// the registration so it can be re-added after an Explorer restart.
func registerTrayIcon() error {
	nid := &notifyIconData{
		hWnd:             tray.win,
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: wmTrayCallback,
		hIcon:            trayIconHandle(),
	}
	tip, _ := syscall.UTF16FromString("KeyRouter — local AI API gateway")
	copy(nid.szTip[:], tip)
	nid.cbSize = uint32(unsafe.Sizeof(*nid))

	res, _, err := pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(nid)))
	if res == 0 {
		return fmt.Errorf("Shell_NotifyIconW failed: %v", err)
	}
	tray.nid = nid
	log.Println("[tray] tray icon registered")
	return nil
}

// trayIconHandle returns the HICON for the tray icon: the app icon extracted
// from the exe (same source as the window/taskbar icon), falling back to the
// generic application icon if extraction fails. The handle lives for the
// whole process — the shell retains it while the icon is in the tray.
func trayIconHandle() uintptr {
	if exePath, err := os.Executable(); err == nil {
		if pathPtr, err := syscall.UTF16PtrFromString(exePath); err == nil {
			var big, small uintptr
			ret, _, _ := extractIconExW.Call(
				uintptr(unsafe.Pointer(pathPtr)), 0,
				uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1,
			)
			if ret != 0 && uint32(ret) != ^uint32(0) {
				if small != 0 {
					// Only the small icon is retained (tray lifetime); the
					// big one is only needed for extraction.
					if big != 0 {
						destroyIcon.Call(big)
					}
					return small
				}
				if big != 0 {
					return big
				}
			}
		}
	}
	icon, _, _ := pLoadIconW.Call(0, idiApplication)
	return icon
}

// trayWndProc handles the tray icon events for the notification window.
func trayWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmTrayCallback:
		switch lParam {
		case wmLButtonUp:
			// Single click: open the main window directly — no menu.
			log.Println("[tray] icon clicked — restoring window")
			showMainWindow()
		case wmRButtonUp:
			showTrayMenu(hwnd)
		}
		return 0
	case tray.taskbarCreated:
		// Explorer restarted: re-register the icon.
		tray.mu.Lock()
		nid := tray.nid
		tray.mu.Unlock()
		if nid != nil {
			pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(nid)))
		}
		return 0
	}
	ret, _, _ := pDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return ret
}

// showMainWindow makes the hidden main window visible and brings it to the
// front (no-op if the close-to-tray handler is not installed yet).
func showMainWindow() {
	tray.mu.Lock()
	ctx := tray.ctx
	tray.mu.Unlock()
	if ctx != nil {
		showTrayWindow(ctx)
	}
}

// showTrayMenu pops up the tray context menu at the cursor and acts on the
// selection. Same items as before: Show KeyRouter / Exit.
func showTrayMenu(hwnd uintptr) {
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	showPtr, _ := syscall.UTF16PtrFromString("Show KeyRouter")
	exitPtr, _ := syscall.UTF16PtrFromString("Exit")
	pAppendMenuW.Call(menu, mfString, menuShowKeyRouter, uintptr(unsafe.Pointer(showPtr)))
	pAppendMenuW.Call(menu, mfSeparator, 0, 0)
	pAppendMenuW.Call(menu, mfString, menuExit, uintptr(unsafe.Pointer(exitPtr)))

	// Make the tray window the foreground window so the menu dismisses when
	// the user clicks elsewhere.
	setForegroundWindow.Call(hwnd)
	var pt struct{ x, y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	cmd, _, _ := pTrackPopupMenu.Call(
		menu, tpmRightButton|tpmReturnCmd,
		uintptr(pt.x), uintptr(pt.y),
		0, hwnd, 0,
	)
	pDestroyMenu.Call(menu)

	switch cmd {
	case menuShowKeyRouter:
		log.Println("[tray] restore window")
		showMainWindow()
	case menuExit:
		requestExit()
	}
}

// requestExit runs the "Exit" flow: confirmation, tray icon removal, real
// window close, and closing the quit channel so StartTray's caller knows the
// app is going down. The exitRequested guard makes it idempotent: the
// confirmation MessageBox is modal but still dispatches the thread's
// messages, so a right-click → Exit → Yes while the dialog is up would
// otherwise run this twice and panic on a double channel close. The same
// applies to a second Exit during the (possibly long) graceful shutdown.
func requestExit() {
	log.Println("[tray] exit requested")
	if !confirmExit() {
		log.Println("[tray] exit cancelled")
		return
	}
	tray.mu.Lock()
	if tray.exitRequested {
		tray.mu.Unlock()
		return
	}
	tray.exitRequested = true
	ctx := tray.ctx
	nid := tray.nid
	tray.nid = nil // icon is being removed; don't re-add it on Explorer restarts
	tray.mu.Unlock()

	if nid != nil {
		pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(nid)))
	}
	if ctx != nil {
		requestTrayQuit(ctx) // posts WM_CLOSE → window really closes
	}
	close(tray.quitChan)
}
