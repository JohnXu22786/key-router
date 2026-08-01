//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// FreeConsole detaches the process from its console window (GUI mode only)
var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var freeConsole = kernel32.NewProc("FreeConsole")

// user32 for the fatal-error message box (the console is detached, so
// log.Fatalf alone would be invisible)
var user32 = syscall.NewLazyDLL("user32.dll")
var messageBoxW = user32.NewProc("MessageBoxW")

// detachConsole detaches from the console window (GUI mode)
func detachConsole() {
	freeConsole.Call()
}

// showFatalError displays a modal error dialog (GUI mode)
func showFatalError(msg string) {
	title, _ := syscall.UTF16PtrFromString("LocalRouter")
	text, _ := syscall.UTF16PtrFromString(msg)
	messageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10 /* MB_ICONERROR */)
}
