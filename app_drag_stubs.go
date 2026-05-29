//go:build !windows

package main

// StartFileDrag 非 Windows 平台桩实现（不执行任何操作）
func (a *App) StartFileDrag(filePath string) {}
