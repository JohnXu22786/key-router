//go:build darwin && cgo

package main

/*
#include <errno.h>
#include <sys/acl.h>
#include <stdlib.h>

static int keyrouter_has_no_extended_acl(const char *path) {
	errno = 0;
	acl_t acl = acl_get_file(path, ACL_TYPE_EXTENDED);
	if (acl == NULL) {
		if (errno == ENOENT) {
			return 1;
		}
		return -(errno != 0 ? errno : EIO);
	}
	acl_free(acl);
	return 0;
}
*/
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

func createPrivateFallbackDataDir(path string) error {
	if err := os.Mkdir(path, 0700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return err
	}
	private, err := darwinHasNoExtendedACL(path)
	if err != nil {
		return err
	}
	if !private {
		return fmt.Errorf("fallback directory has an extended ACL")
	}
	if !darwinDirContentsPrivate(path) {
		return fmt.Errorf("fallback directory changed while securing its ACL")
	}
	return nil
}

func privateFallbackDataDir(path string, info os.FileInfo) bool {
	permissions := info.Mode().Perm()
	if permissions&0077 != 0 || permissions&0700 != 0700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return false
	}
	private, err := darwinHasNoExtendedACL(path)
	return err == nil && private && darwinDirContentsPrivate(path)
}

func validateFallbackTempParent(dir string) error {
	dirs, err := fallbackTempParentDirs(dir)
	if err != nil {
		return err
	}
	for _, path := range dirs {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("temporary path component %q is not a real directory", path)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) {
			return fmt.Errorf("temporary path component %q has an untrusted owner", path)
		}
		if info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("temporary path component %q is writable without sticky protection", path)
		}
		private, err := darwinHasNoExtendedACL(path)
		if err != nil || !private {
			return fmt.Errorf("temporary path component %q has an extended ACL", path)
		}
	}
	return nil
}

func darwinDirContentsPrivate(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		info, err := os.Lstat(filepath.Join(path, entry.Name()))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			return false
		}
	}
	return true
}

func darwinHasNoExtendedACL(path string) (bool, error) {
	pathPtr := C.CString(path)
	defer C.free(unsafe.Pointer(pathPtr))
	result := C.keyrouter_has_no_extended_acl(pathPtr)
	if result < 0 {
		return false, syscall.Errno(-int(result))
	}
	return result == 1, nil
}
