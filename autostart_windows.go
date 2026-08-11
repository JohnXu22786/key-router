//go:build windows

package main

import (
	"log"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// autostart registry key: HKCU\Software\Microsoft\Windows\CurrentVersion\Run
// (per-user, no admin needed).
var (
	advapi32    = syscall.NewLazyDLL("advapi32.dll")
	regOpenKey  = advapi32.NewProc("RegOpenKeyExW")
	regSetValue = advapi32.NewProc("RegSetValueExW")
	regGetValue = advapi32.NewProc("RegGetValueW")
	regCloseKey = advapi32.NewProc("RegCloseKey")
)

const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue   = "KeyRouter"
	// registry value types
	regSZ = 1
	// access rights
	keySetValue = 0x0002
	keyQuery    = 0x0001
	keyRead     = 0x20019
	// RegGetValue flags
	rrfNoExpand = 0x00000004
)

// autostartAppPath returns the executable path used for the Run entry.
func autostartAppPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Clean(exe)
}

// setAutostartEnabled enables (true) or disables (false) launching KeyRouter
// at user login via the HKCU Run key.
func setAutostartEnabled(enabled bool) error {
	appPath := autostartAppPath()
	if enabled && appPath == "" {
		return os.ErrNotExist
	}

	var hkey uintptr
	// Open (create) the Run key for writing.
	// KEY_SET_VALUE | KEY_QUERY_VALUE | KEY_CREATE_SUB_KEY
	ret, _, _ := regOpenKey.Call(
		uintptr(0x80000001), // HKEY_CURRENT_USER
		uintptr(unsafePtr(runKeyPath)),
		0,
		uintptr(0x20006), // KEY_SET_VALUE | KEY_QUERY_VALUE
		uintptr(unsafe.Pointer(&hkey)),
	)
	if ret != 0 {
		return syscall.Errno(ret)
	}
	defer regCloseKey.Call(hkey)

	if enabled {
		pathPtr, _ := syscall.UTF16PtrFromString(appPath)
		ret, _, _ = regSetValue.Call(
			hkey,
			uintptr(unsafePtr(runValue)),
			0,
			uintptr(regSZ),
			uintptr(unsafe.Pointer(pathPtr)),
			uintptr(len(appPath)*2+2), // bytes incl. null terminator
		)
		if ret != 0 {
			return syscall.Errno(ret)
		}
		log.Printf("[autostart] enabled: %s", appPath)
	} else {
		// Delete the value by setting an empty SZ.
		empty := uintptr(0)
		ret, _, _ = regSetValue.Call(
			hkey,
			uintptr(unsafePtr(runValue)),
			0,
			uintptr(regSZ),
			empty,
			0,
		)
		if ret != 0 && syscall.Errno(ret) != syscall.ERROR_FILE_NOT_FOUND {
			return syscall.Errno(ret)
		}
		log.Println("[autostart] disabled")
	}
	return nil
}

// autostartEnabled reports whether the Run entry currently points at this
// executable.
func autostartEnabled() bool {
	appPath := autostartAppPath()
	if appPath == "" {
		return false
	}

	var hkey uintptr
	ret, _, _ := regOpenKey.Call(
		uintptr(0x80000001),
		uintptr(unsafePtr(runKeyPath)),
		0,
		uintptr(keyRead),
		uintptr(unsafe.Pointer(&hkey)),
	)
	if ret != 0 {
		return false
	}
	defer regCloseKey.Call(hkey)

	// RegGetValueW with a null buffer returns ERROR_MORE_DATA (234) and the
	// required size — NOT an error. ERROR_FILE_NOT_FOUND means no entry.
	var size uint32 = 0
	ret, _, _ = regGetValue.Call(
		hkey,
		0,
		uintptr(unsafePtr(runValue)),
		uintptr(rrfNoExpand),
		0,
		0,
		uintptr(unsafe.Pointer(&size)),
	)
	if syscall.Errno(ret) == syscall.ERROR_FILE_NOT_FOUND {
		return false // no Run entry
	}
	if syscall.Errno(ret) != syscall.ERROR_MORE_DATA && ret != 0 {
		return false
	}
	if size == 0 {
		return false
	}
	buf := make([]uint16, size/2+1)
	ret, _, _ = regGetValue.Call(
		hkey,
		0,
		uintptr(unsafePtr(runValue)),
		uintptr(rrfNoExpand),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 {
		return false
	}
	existing := syscall.UTF16ToString(buf)
	return existing != "" && filepath.Clean(existing) == appPath
}

// unsafePtr converts a Go string to a uintptr for the syscall shims above.
func unsafePtr(s string) uintptr {
	p, _ := syscall.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}
