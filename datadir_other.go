//go:build !unix && !windows

package main

import (
	"fmt"
	"os"
)

func createPrivateFallbackDataDir(path string) error {
	return os.Mkdir(path, 0700)
}

func privateFallbackDataDir(_ string, info os.FileInfo) bool {
	return info.Mode().Perm()&0077 == 0
}

func validateFallbackTempParent(string) error {
	return fmt.Errorf("secure temporary fallback is unsupported on this platform")
}
