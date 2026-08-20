//go:build windows

package main

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"testing"
	"unsafe"
)

// Test helpers drive a real Win32 top-level window through the tray subclass
// lifecycle, mirroring the repo's other Windows round-trip tests (e.g. the
// autostart Run-key test) which exercise the actual OS plumbing rather than
// mocks. The window-class procs and wndClassExW struct are shared with the
// tray implementation in tray_windows.go.

var (
	testIsWindow       = user32.NewProc("IsWindow")
	testDestroyWindow  = user32.NewProc("DestroyWindow")
	testClassAtomCount uint32
)

const (
	wsOverlappedWindow = 0x00CF0000 // WS_OVERLAPPEDWINDOW
	// testSubclassID is a distinctive nonzero id used by the direct
	// SetWindowSubclass/RemoveWindowSubclass contract test; it is far outside
	// installTrayCloseHandler's incrementing id range so the two can't clash.
	testSubclassID = uintptr(0x600DC0DE)
)

// createSubclassTestWindow registers a throwaway window class whose WndProc is
// DefWindowProc (so a WM_CLOSE the subclass does NOT swallow destroys the
// window) and creates an owned hidden top-level window using it.
func createSubclassTestWindow(t *testing.T) uintptr {
	t.Helper()
	hinst, _, _ := pGetModuleHandle.Call(0)
	if hinst == 0 {
		t.Fatal("GetModuleHandleW(0) returned 0")
	}
	className := "KRSubclassTest" + fmt.Sprint(atomic.AddUint32(&testClassAtomCount, 1))
	classNamePtr, _ := syscall.UTF16PtrFromString(className)
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   pDefWindowProcW.Addr(),
		hInstance:     hinst,
		lpszClassName: classNamePtr,
	}
	atom, _, _ := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		t.Fatalf("RegisterClassExW(%s) failed", className)
	}

	titlePtr, _ := syscall.UTF16PtrFromString("KRSubclassTest")
	hwnd, _, _ := pCreateWindowExW.Call(
		0, // dwExStyle
		uintptr(unsafe.Pointer(classNamePtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		wsOverlappedWindow,
		10, 10, 100, 100,
		0, 0, hinst, 0,
	)
	if hwnd == 0 {
		t.Fatalf("CreateWindowExW(%s) failed", className)
	}
	return hwnd
}

// TestSubclassCleanupAfterWindowDestroy is the regression test for the leaked
// subclassRefs entry: installTrayCloseHandler records a context for every
// window, and the WM_DESTROY handler must drop it so the table does not grow
// per window created. Before the fix the entry was never removed (the map
// leaked for every window), which is the observable failure caused by passing
// dwRefData (always 0) as RemoveWindowSubclass's uIdSubclass so the removal
// never matched. (Whether the OS-side removal matches is witnessed directly by
// TestRemoveWindowSubclassRequiresRegisteredID; after WM_DESTROY deletes the
// map entry a still-installed subclass is inert, so it is not observable from
// window behavior.)
func TestSubclassCleanupAfterWindowDestroy(t *testing.T) {
	hwnd := createSubclassTestWindow(t)
	defer testDestroyWindow.Call(hwnd)

	var hideCalls int32
	ctx := installTrayCloseHandler(hwnd, func() { atomic.AddInt32(&hideCalls, 1) })

	// installTrayCloseHandler registered the subclass under the just-incremented id.
	subclassRefsMu.Lock()
	id := nextSubclassID - 1
	installed := subclassRefs[id]
	subclassRefsMu.Unlock()
	if installed != ctx {
		t.Fatalf("installTrayCloseHandler did not record subclassRefs[%d]; got %v want %v", id, installed, ctx)
	}

	// The subclass is live: a close without quit is swallowed (window stays
	// alive, onHide fires).
	sendMessageW.Call(hwnd, wmClose, 0, 0)
	if stillAlive, _, _ := testIsWindow.Call(hwnd); stillAlive == 0 {
		t.Fatal("window destroyed by a non-quit WM_CLOSE — subclass did not intercept the close")
	}
	if got := atomic.LoadInt32(&hideCalls); got != 1 {
		t.Fatalf("onHide called %d times after one non-quit WM_CLOSE, want 1", got)
	}

	// A quit close really destroys the window, running the WM_DESTROY cleanup.
	ctx.mu.Lock()
	ctx.quit = true
	ctx.mu.Unlock()
	sendMessageW.Call(hwnd, wmClose, 0, 0)
	if stillAlive, _, _ := testIsWindow.Call(hwnd); stillAlive != 0 {
		t.Fatal("window still alive after quit-close: WM_DESTROY path never ran")
	}

	// The map entry must have been dropped by the WM_DESTROY handler.
	subclassRefsMu.Lock()
	_, leaked := subclassRefs[id]
	subclassRefsMu.Unlock()
	if leaked {
		t.Fatalf("subclassRefs[%d] leaked after window destroy — handler never deleted the entry", id)
	}
}

// TestRemoveWindowSubclassRequiresRegisteredID pins down the comctl32 contract
// the WM_DESTROY fix relies on: SetWindowSubclass is registered with
// (cb, id, dwRefData=0), so RemoveWindowSubclass must be passed the same id.
// Passing the dwRefData value (always 0) never matches — that is exactly why
// the pre-fix removal was a provable no-op.
func TestRemoveWindowSubclassRequiresRegisteredID(t *testing.T) {
	hwnd := createSubclassTestWindow(t)
	defer testDestroyWindow.Call(hwnd)

	cb := syscall.NewCallback(traySubclassProc)
	setWindowSubclass.Call(hwnd, cb, testSubclassID, 0)

	// The buggy call, with dwRefData (0) as uIdSubclass: must NOT match.
	if ret, _, _ := removeWindowSubclass.Call(hwnd, cb, 0); ret != 0 {
		t.Fatalf("RemoveWindowSubclass with uIdSubclass=0 unexpectedly matched a subclass registered as %d (ret=%d)", testSubclassID, ret)
	}
	// The fixed call, with the registered id: must match exactly once.
	if ret, _, _ := removeWindowSubclass.Call(hwnd, cb, testSubclassID); ret == 0 {
		t.Fatalf("RemoveWindowSubclass with the registered id %d failed (ret=0)", testSubclassID)
	}
	if ret, _, _ := removeWindowSubclass.Call(hwnd, cb, testSubclassID); ret != 0 {
		t.Fatalf("RemoveWindowSubclass matched the same id twice (ret=%d); subclass should have been removed already", ret)
	}
}
