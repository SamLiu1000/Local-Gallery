package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:static
var assets embed.FS

func main() {
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

	// 读取上次保存的窗口状态
	width, height := 800, 600
	if ws := LoadWindowState(userDataDir); ws != nil {
		width, height = ws.Width, ws.Height
	}

	// 初始化 libvips
	initVips()

	// 创建 App 实例
	app := NewApp(userDataDir, defaultUserDataDir)

	// 创建 Wails 应用
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
				fmt.Println("[启动任务] 开始后台启动任务...")
				app.ensureImageIndex()
				fmt.Println("[启动任务] ensureImageIndex 完成（已包含增量刷新）")
				// app.repairSearchIndex()
				fmt.Println("[启动任务] 全部后台启动任务完成")
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

	if err != nil {
		log.Fatal(err)
	}
}
