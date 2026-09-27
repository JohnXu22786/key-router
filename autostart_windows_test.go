//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"syscall"
	"testing"
	"unsafe"
)

// regDeleteValue is only needed by the test's restore helper (production
// disabling leaves an empty value in place).
var regDeleteValue = advapi32.NewProc("RegDeleteValueW")

var (
	testCommandLineToArgvW = syscall.NewLazyDLL("shell32.dll").NewProc("CommandLineToArgvW")
	testLocalFree          = syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree")
	testRegDeleteKey       = advapi32.NewProc("RegDeleteKeyW")
)

func TestAutostartRunValueParsesAsSingleExecutable(t *testing.T) {
	path := `C:\Program Files\Key Router\KeyRouter.exe`
	want, err := syscall.UTF16FromString(path)
	if err != nil {
		t.Fatal(err)
	}
	command, err := syscall.UTF16PtrFromString(autostartRunValue(path))
	if err != nil {
		t.Fatal(err)
	}

	var argc int32
	argv, _, _ := testCommandLineToArgvW.Call(uintptr(unsafe.Pointer(command)), uintptr(unsafe.Pointer(&argc)))
	if argv == 0 {
		t.Fatal("CommandLineToArgvW failed to parse the Run value")
	}
	defer testLocalFree.Call(argv)
	if argc != 1 {
		t.Fatalf("parsed %d arguments, want one executable argument", argc)
	}
	args := unsafe.Slice((**uint16)(unsafe.Pointer(argv)), int(argc))
	if got := syscall.UTF16ToString(unsafe.Slice(args[0], len(want))); got != path {
		t.Fatalf("parsed executable = %q, want %q", got, path)
	}
}

func TestAutostartRunValueMatchesExecutable(t *testing.T) {
	appPath := `C:\Program Files\Key Router\KeyRouter.exe`
	tests := []struct {
		name     string
		runValue string
		want     bool
	}{
		{name: "quoted exact path", runValue: `"C:\Program Files\Key Router\KeyRouter.exe"`, want: true},
		{name: "quoted path with different casing", runValue: `"c:\PROGRAM FILES\key router\KEYROUTER.EXE"`, want: true},
		{name: "unquoted legacy path", runValue: appPath, want: true},
		{name: "unquoted legacy path with different casing", runValue: `c:\PROGRAM FILES\key router\KEYROUTER.EXE`, want: true},
		{name: "different executable", runValue: `"C:\Program Files\Key Router\Other.exe"`, want: false},
		{name: "empty Run value", runValue: "", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := autostartRunValueMatchesExecutable(test.runValue, appPath); got != test.want {
				t.Errorf("autostartRunValueMatchesExecutable(%q, %q) = %t, want %t", test.runValue, appPath, got, test.want)
			}
		})
	}
}

func TestAutostartCreatesMissingRunKey(t *testing.T) {
	parentPath := `Software\Microsoft\Windows\CurrentVersion`
	var parentKey uintptr
	ret, _, _ := regOpenKey.Call(
		uintptr(0x80000001), // HKEY_CURRENT_USER
		uintptr(unsafePtr(parentPath)),
		0,
		uintptr(keyRead),
		uintptr(unsafe.Pointer(&parentKey)),
	)
	if ret != 0 {
		t.Fatalf("cannot verify test parent key exists; refusing to create ancestors: %v", syscall.Errno(ret))
	}
	regCloseKey.Call(parentKey)

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	keyPath := `Software\Microsoft\Windows\CurrentVersion\KeyRouterAutostartTest-` + hex.EncodeToString(nonce[:])

	var hkey uintptr
	ret, _, _ = regOpenKey.Call(
		uintptr(0x80000001), // HKEY_CURRENT_USER
		uintptr(unsafePtr(keyPath)),
		0,
		uintptr(keyRead),
		uintptr(unsafe.Pointer(&hkey)),
	)
	if ret == 0 {
		regCloseKey.Call(hkey)
		t.Fatal("generated test key already exists; refusing to alter it")
	}
	if syscall.Errno(ret) != syscall.ERROR_FILE_NOT_FOUND {
		t.Fatalf("cannot verify test key is absent: %v", syscall.Errno(ret))
	}

	t.Cleanup(func() {
		var cleanupKey uintptr
		ret, _, _ := regOpenKey.Call(
			uintptr(0x80000001), // HKEY_CURRENT_USER
			uintptr(unsafePtr(keyPath)),
			0,
			uintptr(keySetValue),
			uintptr(unsafe.Pointer(&cleanupKey)),
		)
		if syscall.Errno(ret) == syscall.ERROR_FILE_NOT_FOUND {
			return
		}
		if ret != 0 {
			t.Errorf("open isolated test key for cleanup: %v", syscall.Errno(ret))
			return
		}
		ret, _, _ = regDeleteValue.Call(cleanupKey, uintptr(unsafePtr(runValue)))
		regCloseKey.Call(cleanupKey)
		if ret != 0 && syscall.Errno(ret) != syscall.ERROR_FILE_NOT_FOUND {
			t.Errorf("delete isolated test value: %v", syscall.Errno(ret))
			return
		}
		ret, _, _ = testRegDeleteKey.Call(uintptr(0x80000001), uintptr(unsafePtr(keyPath)))
		if ret != 0 && syscall.Errno(ret) != syscall.ERROR_FILE_NOT_FOUND {
			t.Errorf("delete isolated test key: %v", syscall.Errno(ret))
		}
	})

	if err := setAutostartEnabledAt(true, keyPath); err != nil {
		t.Fatalf("setAutostartEnabledAt(true) failed: %v", err)
	}
	got, existed, err := readAutostartRaw(keyPath)
	if err != nil {
		t.Fatalf("read created test Run value: %v", err)
	}
	want := autostartRunValue(autostartAppPath())
	if !existed || got != want {
		t.Fatalf("created Run value = (%q, %t), want (%q, true)", got, existed, want)
	}
}

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
	saved, existed, err := readAutostartRaw(runKeyPath)
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
func readAutostartRaw(keyPath string) (string, bool, error) {
	var hkey uintptr
	ret, _, _ := regOpenKey.Call(
		uintptr(0x80000001), // HKEY_CURRENT_USER
		uintptr(unsafePtr(keyPath)),
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
			autostartRegistryStringSize(saved), // bytes incl. null terminator
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
