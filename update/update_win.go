//go:build windows

package update

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// hideWindowAttr hides the console window of the batch helpers (the app is
// a GUI; a visible cmd window mid-update would let the user kill the flow).
func hideWindowAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}

var (
	shell32         = syscall.NewLazyDLL("shell32.dll")
	shellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

// errorCancelled is ERROR_CANCELLED (1223) — the user declined the UAC
// prompt. syscall does not export it, so it is defined here.
const errorCancelled = syscall.Errno(1223)

// shellExecuteInfo mirrors SHELLEXECUTEINFOW. On x64 the API pads members to
// pointer alignment, which matches Go's natural struct alignment, so the
// layout is identical without explicit packing.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     uintptr
}

// runAsElevated launches a file (e.g. the NSIS installer) with the "runas"
// verb so the UAC prompt appears and the installer can write to Program
// Files. Returns nil when the elevated process was created; a declined
// prompt is reported as ErrUpdateCancelled (the app must stay open then).
func runAsElevated(path, args string) error {
	return shellExecute("runas", path, args)
}

// shellExecute launches a file with the given verb via ShellExecuteExW. The
// "open" verb is used by tests (no elevation prompt); "runas" is the real
// updater path. Unlike ShellExecuteW, the call reports the actual outcome:
// the error code (e.g. ERROR_CANCELLED) is retrievable on failure.
//
// The target file is checked first: ShellExecuteW happily reports "success"
// for a missing target (the updater would claim the installer was launched
// while nothing started), and ShellExecuteExW can block for minutes on one.
func shellExecute(verb, path, args string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("target file not found: %w", err)
	}
	v, err := syscall.UTF16PtrFromString(verb)
	if err != nil {
		return err
	}
	f, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	p, err := syscall.UTF16PtrFromString(args)
	if err != nil {
		return err
	}
	info := &shellExecuteInfo{
		cbSize:       uint32(unsafe.Sizeof(shellExecuteInfo{})),
		lpVerb:       v,
		lpFile:       f,
		lpParameters: p,
		nShow:        1, // SW_SHOWNORMAL
	}
	ret, _, _ := shellExecuteExW.Call(uintptr(unsafe.Pointer(info)))
	if ret == 0 {
		// hInstApp carries the error code when the call fails.
		errno := syscall.Errno(info.hInstApp)
		if errno == errorCancelled {
			return ErrUpdateCancelled
		}
		return errno
	}
	return nil
}
