//go:build windows

package main

import (
	"syscall"
	"testing"
	"unsafe"
)

// regDeleteValue is only needed by the test's restore helper (production
// disabling leaves an empty value in place).
var regDeleteValue = advapi32.NewProc("RegDeleteValueW")

// TestAutostartRoundTrip exercises the full enable → read-back → disable cycle
// against the real HKCU Run key, preserving whatever entry existed before.
// Regression: the Settings "Launch at Login" toggle flipped back to off after
// navigating away because autostartEnabled() always reported false — the
// RegGetValueW flag set restricted reads to REG_EXPAND_SZ values, so a
// correctly written REG_SZ entry returned ERROR_UNSUPPORTED_TYPE (1630).
func TestAutostartRoundTrip(t *testing.T) {
	// Snapshot the existing Run entry so the test never silently disables a
	// developer's real launch-at-login setting (or leaves a stale entry
	// pointing at the deleted test binary). If the value cannot be read
	// reliably, abort BEFORE mutating anything: a failed probe must never be
	// treated as "no entry", or the restore would delete a real entry.
	// (A hard kill between snapshot and restore can still lose the entry —
	// inherent to touching the real registry.)
	saved, existed, err := readAutostartRaw()
	if err != nil {
		t.Fatalf("cannot snapshot current Run value, aborting before any mutation: %v", err)
	}
	if existed {
		t.Logf("captured pre-existing Run value: %q", saved)
	} else {
		t.Log("no pre-existing Run value")
	}
	defer func() {
		if err := restoreAutostartRaw(saved, existed); err != nil {
			t.Errorf("restore of original Run value failed: %v", err)
		}
	}()

	if err := setAutostartEnabled(true); err != nil {
		t.Fatalf("setAutostartEnabled(true) failed: %v", err)
	}
	if !autostartEnabled() {
		t.Fatal("autostartEnabled() = false immediately after enabling: Run key was not written or cannot be read back")
	}

	if err := setAutostartEnabled(false); err != nil {
		t.Fatalf("setAutostartEnabled(false) failed: %v", err)
	}
	if autostartEnabled() {
		t.Fatal("autostartEnabled() = true after disabling")
	}
}

// readAutostartRaw returns the raw Run-key value data and whether the value
// exists, or an error when the value could not be read reliably. Only
// ERROR_FILE_NOT_FOUND (no entry) and a readable value are non-error results;
// anything else must make the caller abort rather than guess, so the restore
// never deletes a value that exists but just could not be read.
func readAutostartRaw() (string, bool, error) {
	var hkey uintptr
	ret, _, _ := regOpenKey.Call(
		uintptr(0x80000001), // HKEY_CURRENT_USER
		uintptr(unsafePtr(runKeyPath)),
		0,
		uintptr(keyRead),
		uintptr(unsafe.Pointer(&hkey)),
	)
	if ret != 0 {
		return "", false, syscall.Errno(ret)
	}
	defer regCloseKey.Call(hkey)

	var size uint32 = 0
	ret, _, _ = regGetValue.Call(
		hkey,
		0,
		uintptr(unsafePtr(runValue)),
		uintptr(rrfRtRegSz|rrfNoExpand),
		0,
		0,
		uintptr(unsafe.Pointer(&size)),
	)
	if syscall.Errno(ret) == syscall.ERROR_FILE_NOT_FOUND {
		return "", false, nil // no Run entry
	}
	if syscall.Errno(ret) != syscall.ERROR_MORE_DATA && ret != 0 {
		return "", false, syscall.Errno(ret) // e.g. ERROR_UNSUPPORTED_TYPE
	}
	if size == 0 {
		return "", false, nil // effectively empty → restore deletes
	}
	buf := make([]uint16, size/2+1)
	ret, _, _ = regGetValue.Call(
		hkey,
		0,
		uintptr(unsafePtr(runValue)),
		uintptr(rrfRtRegSz|rrfNoExpand),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 {
		return "", false, syscall.Errno(ret)
	}
	return syscall.UTF16ToString(buf), true, nil
}

// restoreAutostartRaw writes the captured value back verbatim, or deletes the
// value when no entry existed before the test.
func restoreAutostartRaw(saved string, existed bool) error {
	var hkey uintptr
	ret, _, _ := regOpenKey.Call(
		uintptr(0x80000001), // HKEY_CURRENT_USER
		uintptr(unsafePtr(runKeyPath)),
		0,
		uintptr(0x20006), // KEY_WRITE
		uintptr(unsafe.Pointer(&hkey)),
	)
	if ret != 0 {
		return syscall.Errno(ret)
	}
	defer regCloseKey.Call(hkey)

	if existed {
		p, _ := syscall.UTF16PtrFromString(saved)
		ret, _, _ = regSetValue.Call(
			hkey,
			uintptr(unsafePtr(runValue)),
			0,
			uintptr(regSZ),
			uintptr(unsafe.Pointer(p)),
			uintptr(len(saved)*2+2), // bytes incl. null terminator
		)
	} else {
		ret, _, _ = regDeleteValue.Call(hkey, uintptr(unsafePtr(runValue)))
		if syscall.Errno(ret) == syscall.ERROR_FILE_NOT_FOUND {
			return nil
		}
	}
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}
