package main

// ============================================================
// 文件夹切换 debug 日志
//
// 目的：当用户点击文件夹时，把"前后端每一步 + 每一步耗时"逐行写入
//   user/debug-switch.log（与启动截图同目录），用于定位"切换文件夹后
//   图廊迟迟不出图 / 卡在某一步"的确切位置。
//
// 使用：
//   1) main() 里调用 app.InitDebugSwitchLog()（写一份在用户数据目录）；
//   2) Go 侧在关键步骤调用 debugSwitchf("...", ...)；
//   3) 前端 JS 通过 WailsBridge.debugSwitchLog(msg) /
//      WailsBridge.debugSwitchLogBatch(lines) 写入同一文件（前端加 +ms 前缀）。
//   ★ 该日志与启动截图独立，避免 debug 输出把 startup.log 污染；内容偏长，
//     按需在排障时开启（InitDebugSwitchLog 无条件开启，改由文件存在与否决定）。
// ============================================================

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	debugSwitchFileMu sync.Mutex
	debugSwitchFile   *os.File
	debugSwitchT0     = time.Now()
)

// InitDebugSwitchLog 在用户数据目录开启 debug-switch.log。可重复调用（内部同步）。
func (a *App) InitDebugSwitchLog() {
	debugSwitchFileMu.Lock()
	defer debugSwitchFileMu.Unlock()
	if debugSwitchFile != nil {
		debugSwitchFile.Close()
		debugSwitchFile = nil
	}
	path := filepath.Join(a.userDataDir, "debug-switch.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("[debug-switch] 无法开启日志 %s: %v\n", path, err)
		return
	}
	debugSwitchFile = f
	debugSwitchT0 = time.Now()
	debugSwitchfNoLock("=== debug-switch 日志开启: %s ===", path)
}

// debugSwitchf 追加一行带时间戳与相对启动耗时(秒)的日志。写后 sync，便于实时 tail。
func debugSwitchf(format string, args ...interface{}) {
	debugSwitchFileMu.Lock()
	defer debugSwitchFileMu.Unlock()
	debugSwitchfNoLock(format, args...)
}

func debugSwitchfNoLock(format string, args ...interface{}) {
	if debugSwitchFile == nil {
		return
	}
	msg := fmt.Sprintf("%s [+%9.3fs] %s\n", time.Now().Format("15:04:05.000"), time.Since(debugSwitchT0).Seconds(), fmt.Sprintf(format, args...))
	debugSwitchFile.WriteString(msg)
	debugSwitchFile.Sync()
}

// DebugSwitchLog 前端单行写入：JS 在文件夹切换的每一步调用。
func (a *App) DebugSwitchLog(msg string) {
	debugSwitchf("JS: %s", msg)
}

// DebugSwitchLogBatch 前端批量写入：JS 先攒一批再一次性发，避免每条一次 websocket。
func (a *App) DebugSwitchLogBatch(lines []string) {
	for _, l := range lines {
		debugSwitchf("JS: %s", l)
	}
}
