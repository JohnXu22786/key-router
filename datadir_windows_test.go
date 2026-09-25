//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsFallbackRejectsPermissiveACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fallback")
	if err := ensurePrivateFallbackDataDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !privateFallbackDataDir(dir, info) {
		t.Fatal("new fallback directory did not have a private ACL")
	}

	setEveryoneDACL(t, dir)
	info, err = os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if privateFallbackDataDir(dir, info) {
		t.Fatal("fallback validator accepted a directory whose ACL grants Everyone access")
	}
	if ensurePrivateFallbackDataDir(dir) == nil {
		t.Fatal("fallback accepted a directory whose ACL grants Everyone access")
	}
}

func TestWindowsFallbackRejectsPermissiveChildACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fallback")
	if err := ensurePrivateFallbackDataDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	logPath := filepath.Join(dir, "key-router.log")
	if err := os.WriteFile(logPath, []byte("log"), 0600); err != nil {
		t.Fatal(err)
	}
	setEveryoneDACL(t, logPath)
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if privateFallbackDataDir(dir, info) {
		t.Fatal("fallback validator accepted a log whose ACL grants Everyone access")
	}
	if ensurePrivateFallbackDataDir(dir) == nil {
		t.Fatal("fallback accepted a log whose ACL grants Everyone access")
	}
}

func TestWindowsFallbackRejectsMutableTempParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	setEveryoneDACL(t, parent)
	if err := validateFallbackTempParent(parent); err == nil {
		t.Fatal("fallback accepted a temp parent that lets Everyone replace children")
	}
}

func TestWindowsFallbackAllowsNonReplaceableTempParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;0x4;;;WD)(A;OICIIO;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateFallbackTempParent(parent); err != nil {
		t.Fatalf("fallback rejected a parent that grants only sibling creation and inherit-only rights: %v", err)
	}
}

func setEveryoneDACL(t *testing.T, path string) {
	t.Helper()
	permissive, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := permissive.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}
