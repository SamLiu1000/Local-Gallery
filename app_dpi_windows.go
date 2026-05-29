//go:build windows

package main

import (
	"syscall"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetDpiForSystem  = user32.NewProc("GetDpiForSystem")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
)

const (
	SM_CXSCREEN = 0
	SM_CYSCREEN = 1
)

// getSystemDpi returns the system DPI value (96 = 100%, 144 = 150%, 192 = 200%)
func getSystemDpi() int {
	ret, _, _ := procGetDpiForSystem.Call()
	if ret == 0 {
		return 96 // fallback
	}
	return int(ret)
}

// getScreenWidth returns the current primary screen width in pixels
func getScreenWidth() int {
	ret, _, _ := procGetSystemMetrics.Call(uintptr(SM_CXSCREEN))
	return int(ret)
}

// getScreenHeight returns the current primary screen height in pixels
func getScreenHeight() int {
	ret, _, _ := procGetSystemMetrics.Call(uintptr(SM_CYSCREEN))
	return int(ret)
}
