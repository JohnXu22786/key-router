//go:build windows

package update

import (
	"syscall"
	"unsafe"
)

var (
	shell32       = syscall.NewLazyDLL("shell32.dll")
	shellExecuteW = shell32.NewProc("ShellExecuteW")
)

// runAsElevated launches a file (e.g. the NSIS installer) with the "runas"
// verb so the UAC prompt appears and the installer can write to Program
// Files. Returns nil when the process was successfully started.
func runAsElevated(path, args string) error {
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(path)
	params, _ := syscall.UTF16PtrFromString(args)
	// SW_SHOWNORMAL = 1
	ret, _, _ := shellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		0,
		1,
	)
	// ShellExecute returns a value > 32 on success.
	if ret <= 32 {
		return syscall.Errno(ret)
	}
	return nil
}
