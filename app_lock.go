package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// acquireInstanceLock 单实例保护：防止两个实例同时读写 user-data.json / SQLite（并发覆盖 = 数据丢失）。
//
// ★ 用 Windows 命名互斥体（CreateMutexW）实现：持有进程退出（含崩溃）时系统自动释放互斥体，
//   不存在"残留锁文件 + PID 被回收复用"导致的误判（旧文件锁方案曾因 PID 复用
//   在进程已退出后仍报"已在运行"无法启动）。
// 返回释放函数（正常退出时关闭句柄）。
func acquireInstanceLock(userDataDir string) (func(), error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	createMutex := kernel32.NewProc("CreateMutexW")
	closeHandle := kernel32.NewProc("CloseHandle")

	// 互斥体名按数据目录区分（同名目录只允许一个实例；不同数据目录可并行）
	name := "LocalGallery_Single_" + sanitizeLockName(userDataDir)
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, fmt.Errorf("无法构造实例互斥体名称: %w", err)
	}
	h, _, callErr := createMutex.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		return nil, fmt.Errorf("无法创建实例互斥体")
	}
	if callErr == syscall.Errno(183) { // ERROR_ALREADY_EXISTS：另一个实例已持有
		closeHandle.Call(h)
		return nil, fmt.Errorf("Local Gallery 已在运行")
	}
	fmt.Printf("[单实例] 已获取实例互斥体: %s\n", name)

	// 清理旧版文件锁残留（新方案不再使用，删除以免混淆）
	oldLock := filepath.Join(userDataDir, ".instance.lock")
	_ = os.Remove(oldLock)

	return func() { closeHandle.Call(h) }, nil
}

// sanitizeLockName 把数据目录路径清洗成互斥体名允许的字符（不含反斜杠/冒号）
func sanitizeLockName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// showInstanceConflictMessage 弹窗提示已有实例在运行
func showInstanceConflictMessage() {
	user32 := syscall.NewLazyDLL("user32.dll")
	mb := user32.NewProc("MessageBoxW")
	title, _ := syscall.UTF16PtrFromString("Local Gallery")
	msg, _ := syscall.UTF16PtrFromString("Local Gallery 已经在运行。\n为避免两份程序同时读写数据造成冲突，请使用已打开的窗口。")
	mb.Call(0, uintptr(unsafe.Pointer(msg)), uintptr(unsafe.Pointer(title)), 0x40) // MB_ICONINFORMATION
}
