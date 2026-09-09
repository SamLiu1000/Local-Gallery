package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:static
var assets embed.FS

// ==================== 启动打点日志 ====================
// 定位冷启动慢的瓶颈：每一阶段耗时追加写入 startup.log。
// ★ 写两份：exe 所在目录（总能找到）+ 用户数据目录（跟随数据），避免"找不到日志"。
var startupLogs []*os.File
var startupT0 = time.Now()

func addStartupLog(path string) {
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		startupLogs = append(startupLogs, f)
	}
}

func logStartupf(format string, args ...interface{}) {
	if len(startupLogs) == 0 {
		return
	}
	msg := fmt.Sprintf("%s [+%8.3fs] %s\n", time.Now().Format("15:04:05.000"), time.Since(startupT0).Seconds(), fmt.Sprintf(format, args...))
	for _, f := range startupLogs {
		f.WriteString(msg)
	}
}

func closeStartupLogs() {
	for _, f := range startupLogs {
		f.Close()
	}
	startupLogs = nil
}

func main() {
	startupT0 = time.Now()
	// ★ headless 缩略图生成 worker：由主程序 spawn，独立进程可被 kill，
	//   杀进程 = OS 立即终止其在途的原图读取（等效"关闭程序"）。
	if len(os.Args) > 1 && os.Args[1] == "--thumbgen" {
		port := ""
		concurrency := 0
		for i := 2; i+1 < len(os.Args); i++ {
			switch os.Args[i] {
			case "--port":
				port = os.Args[i+1]
				i++
			case "--concurrency":
				concurrency, _ = strconv.Atoi(os.Args[i+1])
				i++
			}
		}
		runThumbGenWorker(port, concurrency)
		return
	}

	// 获取用户数据目录（基于工作目录；wails dev 下为项目根目录，发布版双击运行时为 exe 所在目录）

	execDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("无法获取当前目录: %v", err)
	}
	defaultUserDataDir := filepath.Join(execDir, "user")
	userDataDir := defaultUserDataDir

	// ★ 检查是否有已保存的自定义用户数据目录（用户通过设置迁移过）
	if customDir := GetSavedUserDataDir(defaultUserDataDir); customDir != "" {
		userDataDir = customDir
	}

	// ★ 启动打点日志：写两份（exe 所在目录 + 用户数据目录），确保总能找到
	addStartupLog(filepath.Join(userDataDir, "startup.log"))
	if exePath, err := os.Executable(); err == nil {
		addStartupLog(filepath.Join(filepath.Dir(exePath), "startup.log"))
	}
	if len(startupLogs) > 0 {
		logStartupf("=== Local Gallery 启动 ===")
		// ★ 打点：记录启动上下文，区分"从不同目录/方式启动"导致的假象
		//   （不同 CWD → 不同 userDataDir → 不同配置，openThumbDB/WebView2 表现截然不同）
		if wd, err := os.Getwd(); err == nil {
			logStartupf("CWD=%s userDataDir=%s args=%v", wd, userDataDir, os.Args[1:])
		}
	}

	// 读取上次保存的窗口状态
	width, height := 800, 600
	if ws := LoadWindowState(userDataDir); ws != nil {
		width, height = ws.Width, ws.Height
	}
	logStartupf("LoadWindowState 完成")

	// 初始化 libvips
	initVips()
	logStartupf("initVips 完成")

	// 创建 App 实例
	app := NewApp(userDataDir, defaultUserDataDir)
	app.InitDebugSwitchLog()
	logStartupf("NewApp 完成")

	// 创建 Wails 应用
	// ★ 打点：窗口+WebView2 初始化阶段（"NewApp 完成" ↔ "OnStartup 触发"之间的盲区）。
	//   若此阶段失败/卡死，日志会停在"开始 wails.Run"而不出现 OnStartup——
	//   这正是"点了没反应、窗口没出来"的根因线索。
	logStartupf("main: 开始 wails.Run（创建窗口+WebView2）")
	err = wails.Run(&options.App{
		Title:     "Local Gallery - AI 图片管理工具",
		Width:     width,
		Height:    height,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		EnableDefaultContextMenu: true,
		OnStartup: func(ctx context.Context) {
			logStartupf("OnStartup 触发（窗口+WebView2 已创建）")
			app.SetContext(ctx)
			// 如果有上次保存的窗口状态，恢复位置和最大化
			if ws := LoadWindowState(userDataDir); ws != nil {
				if ws.Maximised {
					wailsruntime.WindowMaximise(ctx)
				} else {
					go func() {
						time.Sleep(50 * time.Millisecond)
						x, y := ws.X, ws.Y
						// 使用当前屏幕尺寸进行边界钳制
						currentSW := getScreenWidth()
						currentSH := getScreenHeight()
						if currentSW > 0 && currentSH > 0 {
							if x < 0 {
								x = 0
							}
							if y < 0 {
								y = 0
							}
							if x+ws.Width > currentSW {
								x = currentSW - ws.Width
							}
							if y+ws.Height > currentSH {
								y = currentSH - ws.Height
							}
							if x+ws.Width < 200 {
								x = 200 - ws.Width
							}
							if y+ws.Height < 200 {
								y = 200 - ws.Height
							}
						}
						wailsruntime.WindowSetPosition(ctx, x, y)
					}()
				}
			}

			// 后台按顺序执行：补扫缺失 → 增量刷新 → 修复搜索索引
			// ctx 已就绪，增量刷新发现变化时会通过 scan:complete 事件通知前端
			go func() {
				defer func() {
					if r := recover(); r != nil {
						fmt.Printf("[启动任务] PANIC: %v\n", r)
					}
				}()
				logStartupf("[启动任务] 开始后台启动任务...")
				app.ensureImageIndex()
				logStartupf("[启动任务] ensureImageIndex 完成（已包含增量刷新）")
				// app.repairSearchIndex()
				if app.ctx != nil {
					wailsruntime.EventsEmit(app.ctx, "index:ready", nil)
				}
				logStartupf("[启动任务] 全部后台启动任务完成")
			}()
		},
		OnBeforeClose: func(ctx context.Context) bool {
			x, y := wailsruntime.WindowGetPosition(ctx)
			w, h := wailsruntime.WindowGetSize(ctx)
			maximised := wailsruntime.WindowIsMaximised(ctx)
			app.SaveWindowState(w, h, x, y, maximised)
			return false
		},
		OnShutdown: func(ctx context.Context) {
			// ★ 保存当前 thumbCounts，供下次冷启动直接恢复
			app.persistThumbCounts()
			// ★ 杀掉缩略图生成 worker 子进程，防止成为孤儿进程
			app.killThumbWorker()
			// ★ 释放单实例锁
			if app.instanceLockRelease != nil {
				app.instanceLockRelease()
			}
			logStartupf("=== 退出，总耗时 %s ===", time.Since(startupT0).Round(time.Millisecond))
			FlushDebugSwitchLog() // ★ 缓冲日志刷盘（否则最多丢 0.5s 尾部）
			closeStartupLogs()
			shutdownVips()
		},
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			WebviewUserDataPath:  filepath.Join(defaultUserDataDir, "webview2"),
		},
	})

	// ★ 打点：wails.Run 返回。正常退出时 OnShutdown 已先打印"=== 退出 ==="；
	//   若这里 err 非空且日志中从头到尾没有 OnStartup → 窗口创建/WebView2 初始化失败，
	//   即"点了没反应"的确切原因（此前 log.Fatal 只输出到 stderr，日志里完全看不到）。
	logStartupf("main: wails.Run 返回 err=%v（启动后 %s）", err, time.Since(startupT0).Round(time.Millisecond))

	if err != nil {
		log.Fatal(err)
	}
}
