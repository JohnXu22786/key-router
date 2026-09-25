//go:build darwin && !cgo

package main

import (
	"fmt"
	"os"
)

func createPrivateFallbackDataDir(string) error {
	return fmt.Errorf("secure temporary fallback data directory requires macOS ACL support")
}

func privateFallbackDataDir(string, os.FileInfo) bool {
	return false
}

func validateFallbackTempParent(string) error {
	return fmt.Errorf("secure temporary fallback data directory requires macOS ACL support")
}
