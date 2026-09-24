package main

import "unicode/utf16"

func autostartRunValue(executablePath string) string {
	return `"` + executablePath + `"`
}

func autostartRegistryStringSize(value string) uintptr {
	return uintptr((len(utf16.Encode([]rune(value))) + 1) * 2)
}
