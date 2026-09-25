//go:build darwin && cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinFallbackRejectsExtendedACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fallback")
	if err := ensurePrivateFallbackDataDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if output, err := exec.Command("/bin/chmod", "+a", "everyone allow read,write,execute", dir).CombinedOutput(); err != nil {
		t.Fatalf("add extended ACL: %v: %s", err, output)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if ensurePrivateFallbackDataDir(dir) == nil {
		t.Fatal("fallback accepted an extended ACL granting access to everyone")
	}
}

func TestDarwinFallbackRejectsExtendedParentACL(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/bin/chmod", "+a", "everyone allow read,write,execute", parent).CombinedOutput(); err != nil {
		t.Fatalf("add extended ACL: %v: %s", err, output)
	}
	if err := validateFallbackTempParent(parent); err == nil {
		t.Fatal("fallback accepted a temporary parent with an extended ACL")
	}
}
