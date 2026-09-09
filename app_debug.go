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
//   ★ 该日志与启动截图独立，避免 debug 输出把 startup.log 污染。
//   ★ 默认开启；设环境变量 LOCAL_GALLERY_DEBUG_SWITCH=0 关闭（日常使用减少 I/O）。
//
// ★ 写入方式：内存缓冲 + 定时刷盘。
//   此前每行都调用 debugSwitchFile.Sync()（fsync）。一次 20s 的浏览会话实测近
//   2 万行、单行 append+fsync ≈1.4ms（SSD），合计约 25s 串行 I/O，且全程持
//   debugSwitchFileMu —— 缩略图请求路径每张图要写 4 条日志，直接与出图争锁，
//   是"点文件夹后很多图 Load failed"的放大因素之一。改成缓冲后开销可忽略，
//   tail -f 仍有 <0.5s 延迟。
// ============================================================

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	debugSwitchFileMu sync.Mutex
	debugSwitchFile   *os.File
	debugSwitchT0     = time.Now()
	debugSwitchBuf    []byte
	debugSwitchTimer  *time.Timer
)

const (
	debugSwitchFlushInterval = 500 * time.Millisecond // 缓冲刷盘间隔
	debugSwitchFlushAt       = 64 << 10               // 缓冲超过 64KB 立即刷
	debugSwitchRotateAt      = 16 << 20               // 启动时超过 16MB 则轮转，防日志无限增长
)

// debugSwitchEnabled 是否记录文件夹切换日志。默认开启（排障需要）；
// 设 LOCAL_GALLERY_DEBUG_SWITCH=0 关闭。
func debugSwitchEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOCAL_GALLERY_DEBUG_SWITCH"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// InitDebugSwitchLog 在用户数据目录开启 debug-switch.log。可重复调用（内部同步）。
func (a *App) InitDebugSwitchLog() {
	if !debugSwitchEnabled() {
		fmt.Println("[debug-switch] 已通过 LOCAL_GALLERY_DEBUG_SWITCH 关闭")
		return
	}
	debugSwitchFileMu.Lock()
	defer debugSwitchFileMu.Unlock()
	if debugSwitchFile != nil {
		debugSwitchFile.Close()
		debugSwitchFile = nil
	}
	path := filepath.Join(a.userDataDir, "debug-switch.log")
	// ★ 轮转：单文件超过上限就改名保留，本次会话重新开始。
	if info, err := os.Stat(path); err == nil && info.Size() > debugSwitchRotateAt {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("[debug-switch] 无法开启日志 %s: %v\n", path, err)
		return
	}
	debugSwitchFile = f
	debugSwitchBuf = debugSwitchBuf[:0]
	debugSwitchT0 = time.Now()
	debugSwitchfNoLock("=== debug-switch 日志开启: %s ===", path)
}

// debugSwitchf 追加一行带时间戳与相对启动耗时(秒)的日志（进内存缓冲，定时刷盘）。
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
	debugSwitchBuf = append(debugSwitchBuf, msg...)
	if len(debugSwitchBuf) >= debugSwitchFlushAt {
		flushDebugSwitchLocked()
		return
	}
	if debugSwitchTimer == nil {
		debugSwitchTimer = time.AfterFunc(debugSwitchFlushInterval, func() {
			debugSwitchFileMu.Lock()
			debugSwitchTimer = nil
			flushDebugSwitchLocked()
			debugSwitchFileMu.Unlock()
		})
	}
}

// flushDebugSwitchLocked 把缓冲写入文件（不 fsync，落盘时机交给 OS）。
func flushDebugSwitchLocked() {
	if len(debugSwitchBuf) == 0 || debugSwitchFile == nil {
		return
	}
	_, _ = debugSwitchFile.Write(debugSwitchBuf)
	debugSwitchBuf = debugSwitchBuf[:0]
}

// FlushDebugSwitchLog 退出前刷盘并关闭（供 main 的 OnShutdown 调用）。
func FlushDebugSwitchLog() {
	debugSwitchFileMu.Lock()
	defer debugSwitchFileMu.Unlock()
	if debugSwitchTimer != nil {
		debugSwitchTimer.Stop()
		debugSwitchTimer = nil
	}
	flushDebugSwitchLocked()
	if debugSwitchFile != nil {
		_ = debugSwitchFile.Close()
		debugSwitchFile = nil
	}
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
