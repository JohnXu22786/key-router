//go:build unix && !darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func createPrivateFallbackDataDir(path string) error {
	if err := os.Mkdir(path, 0700); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}

func privateFallbackDataDir(path string, info os.FileInfo) bool {
	permissions := info.Mode().Perm()
	if permissions&0077 != 0 || permissions&0700 != 0700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && unixFallbackContentsPrivate(path)
}

func unixFallbackContentsPrivate(path string) bool {
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
	}
	return nil
}
