package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"container/list"

	"local-gallery/internal/database"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"go.etcd.io/bbolt"
)

type App struct {
	ctx                context.Context
	userDataDir        string
	defaultUserDataDir string // 程序启动时的默认路径，用于重置

	images      map[string]*ImageEntry
	folderIndex map[string][]string
	folderCount map[string]int

	// LRU 按文件夹加载缓存
	folderLoaded map[string]bool              // folderKey 是否已加载进 a.images
	folderLRU    *list.List                   // LRU 队列，elem.Value = folderKey
	lruNodes     map[string]*list.Element     // folderKey → LRU 节点

	registeredRoots map[string]bool
	folderTypes     map[string]string // rootPath -> "ai" | "photo" | "mixed"

	userDataFile    string
	windowStateFile string

	httpServer    *http.Server
	httpBaseURL   string
	thumbServer2  *http.Server // 缩略图第二 origin，与主缩略图分流
	thumbBaseURL2 string
	imageServer   *http.Server // 原图独立 origin，不跟缩略图抢连接
	imageBaseURL  string
	lanServer     *http.Server // 局域网 HTTP Server
	lanIP         string
	lanPort       int

	scanningRoots  map[string]bool
	scanMu         sync.Mutex
	bgPaused       atomic.Int32
	switchMu       sync.Mutex // 防止 RestartWithNewPaths 重入
	pendingUserDir string     // SetUserDataDir 设定的待切换路径，供 RestartWithNewPaths 优先读取

	imageDB    *database.ImageDB
	userDataDB *database.UserDataDB
	thumbDB    *bbolt.DB // 缩略图 BoltDB 单文件存储

	indexingRoots  map[string]context.CancelFunc
	indexingRootsMu sync.Mutex

	proxyCancels  map[string]context.CancelFunc
	proxyCancelMu sync.Mutex
	mu            sync.RWMutex
}

// WindowState 保存窗口的位置和大小
type WindowState struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Maximised bool `json:"maximised"`
}

func NewApp(userDataDir, defaultUserDataDir string) *App {
	app := &App{
		userDataDir:        userDataDir,
		defaultUserDataDir: defaultUserDataDir,
		images:             make(map[string]*ImageEntry),
		folderIndex:        make(map[string][]string),
		folderCount:        make(map[string]int),
		folderLoaded:       make(map[string]bool),
		folderLRU:          list.New(),
		lruNodes:           make(map[string]*list.Element),
		registeredRoots:    make(map[string]bool),
		folderTypes:        make(map[string]string),
		userDataFile:       filepath.Join(userDataDir, "user-data.json"),
		windowStateFile:    filepath.Join(userDataDir, "window-state.json"),
		scanningRoots:      make(map[string]bool),
		proxyCancels:       make(map[string]context.CancelFunc),
		indexingRoots:      make(map[string]context.CancelFunc),
	}
	os.MkdirAll(userDataDir, 0755)

	// ★ 启动时检测是否有上次保存的自定义 userDataDir，有则切换
	app.applySavedUserDataDir()

	// 初始化 BoltDB 缩略图存储
	app.openThumbDB()

	// 初始化 SQLite 图片元数据库
	dbPath := filepath.Join(app.userDataDir, "images.db")
	if imageDB, err := database.New(dbPath); err != nil {
		fmt.Printf("[警告] 无法打开图片元数据库: %v，搜索功能将不可用\n", err)
	} else {
		app.imageDB = imageDB
	}

	// 初始化 SQLite 用户数据库（设置数据）
	udbPath := filepath.Join(app.userDataDir, "user-data.db")
	if userDataDB, err := database.NewUserDataDB(udbPath); err != nil {
		fmt.Printf("[警告] 无法打开用户数据库: %v\n", err)
	} else {
		app.userDataDB = userDataDB
	}

	app.loadUserData()
	app.loadThumbSettings()     // 恢复缩略图并发数与缩放算法设置
	app.migratePromptVersions() // migrate old promptVersions to SQLite
	app.migrateUserData() // migrate registeredRoots/imageTags/favorites to SQLite
	app.loadFolderIndexLight() // ★ 轻量索引启动：仅加载文件夹列表+count，图片按需加载

	app.startHTTPServer()

	return app
}

func (a *App) startHTTPServer() {
	port := 19876
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("[HTTP] 端口 %d 被占用，使用随机端口\n", port)
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fmt.Println("[HTTP] 无法监听端口")
			return
		}
		port = listener.Addr().(*net.TCPAddr).Port
	}
	listener.Close()
	a.httpBaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	// 缩略图 mux（主端口）
	thumbMux := http.NewServeMux()

	// 用户自定义图标目录（优先使用），回退到嵌入的静态资源
	userIconsDir := filepath.Join(a.userDataDir, "icons")
	os.MkdirAll(userIconsDir, 0755)
	fmt.Printf("[Icons] 用户图标目录: %s\n", userIconsDir)

	// avatar 目录：保存裁剪后的头像图片
	userAvatarDir := filepath.Join(a.userDataDir, "avatar")
	os.MkdirAll(userAvatarDir, 0755)
	fmt.Printf("[Avatar] 头像目录: %s\n", userAvatarDir)

	// 头像文件服务
	thumbMux.HandleFunc("/avatar/", func(w http.ResponseWriter, r *http.Request) {
		fileName := strings.TrimPrefix(r.URL.Path, "/avatar/")
		if fileName == "" || strings.Contains(fileName, "..") {
			http.NotFound(w, r)
			return
		}
		avatarPath := filepath.Join(userAvatarDir, fileName)
		data, err := os.ReadFile(avatarPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		w.Write(data)
	})

	// 图标服务：如果用户目录下存在同名图标则使用，否则使用嵌入的默认图标
	thumbMux.HandleFunc("/icons/", func(w http.ResponseWriter, r *http.Request) {
		iconName := strings.TrimPrefix(r.URL.Path, "/icons/")
		if iconName == "" || strings.Contains(iconName, "..") {
			http.NotFound(w, r)
			return
		}
		// 优先检查用户目录
		userPath := filepath.Join(userIconsDir, iconName)
		if data, err := os.ReadFile(userPath); err == nil {
			fmt.Printf("[Icons] 提供用户自定义图标: %s\n", iconName)
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Cache-Control", "no-cache")
			w.Write(data)
			return
		}
		// 回退到嵌入资源
		embedPath := "static/icons/" + iconName
		data, err := assets.ReadFile(embedPath)
		if err == nil {
			fmt.Printf("[Icons] 提供默认嵌入图标: %s\n", iconName)
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Write(data)
			return
		}
		fmt.Printf("[Icons] 图标未找到: %s (userPath=%s)\n", iconName, userPath)
		http.NotFound(w, r)
	})
	thumbMux.HandleFunc("/thumb/", func(w http.ResponseWriter, r *http.Request) {
		imageID := strings.TrimPrefix(r.URL.Path, "/thumb/")
		jpegBytes, err := a.serveThumbnail(imageID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpegBytes)
	})
	a.httpServer = &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: thumbMux}
	go func() {
		fmt.Printf("[HTTP] 缩略图服务: %s\n", a.httpBaseURL)
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[HTTP] 缩略图服务错误: %v\n", err)
		}
	}()

	// ★ 缩略图第二 origin，浏览器再分配 6 连接池，缩略图共 12 连接
	thumb2Listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("[HTTP] 缩略图第二服务启动失败:", err)
	} else {
		thumb2Port := thumb2Listener.Addr().(*net.TCPAddr).Port
		a.thumbBaseURL2 = fmt.Sprintf("http://127.0.0.1:%d", thumb2Port)
		thumb2Mux := http.NewServeMux()
		thumb2Mux.HandleFunc("/thumb/", func(w http.ResponseWriter, r *http.Request) {
			imageID := strings.TrimPrefix(r.URL.Path, "/thumb/")
			jpegBytes, err := a.serveThumbnail(imageID)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(jpegBytes)
		})
		a.thumbServer2 = &http.Server{Handler: thumb2Mux}
		go func() {
			fmt.Printf("[HTTP] 缩略图第二服务: %s\n", a.thumbBaseURL2)
			if err := a.thumbServer2.Serve(thumb2Listener); err != nil && err != http.ErrServerClosed {
				fmt.Printf("[HTTP] 缩略图第二服务错误: %v\n", err)
			}
		}()
	}

	// ★ 原图独立 origin，浏览器分配独立 6 连接池，不与缩略图排队
	imageListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("[HTTP] 原图服务启动失败:", err)
	} else {
		imagePort := imageListener.Addr().(*net.TCPAddr).Port
		a.imageBaseURL = fmt.Sprintf("http://127.0.0.1:%d", imagePort)
		imageMux := http.NewServeMux()
		imageMux.HandleFunc("/image/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			imageID := strings.TrimPrefix(r.URL.Path, "/image/")
			imagePath := a.resolveImagePath(imageID)
			if imagePath == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=600")
			w.Header().Set("Content-Type", getMIMEType(filepath.Ext(imagePath)))
			w.Header().Set("Accept-Ranges", "bytes")
			http.ServeFile(w, r, imagePath)
		})
		a.imageServer = &http.Server{Handler: imageMux}
		go func() {
			fmt.Printf("[HTTP] 原图服务: %s\n", a.imageBaseURL)
			if err := a.imageServer.Serve(imageListener); err != nil && err != http.ErrServerClosed {
				fmt.Printf("[HTTP] 原图服务错误: %v\n", err)
			}
		}()
	}

	// ★ 局域网 Server（0.0.0.0），手机/平板可通过 WiFi 访问
	// LAN Server now controlled by frontend
}

func (a *App) GetHTTPBaseURL() string { return a.httpBaseURL }

// SaveAvatar 将 base64 JPEG 数据保存为 avatar 文件，返回 URL 路径（如 /avatar/abc123.jpg）
func (a *App) SaveAvatar(base64Data string) (string, error) {
	// 去掉 data:image/jpeg;base64, 前缀
	prefix := "data:image/jpeg;base64,"
	raw := base64Data
	if strings.HasPrefix(raw, prefix) {
		raw = strings.TrimPrefix(raw, prefix)
	}
	// 兼容不带前缀的纯 base64
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("解码 base64 失败: %w", err)
	}

	avatarDir := filepath.Join(a.userDataDir, "avatar")
	os.MkdirAll(avatarDir, 0755)

	fileName := fmt.Sprintf("avatar_%d.jpg", time.Now().UnixNano())
	filePath := filepath.Join(avatarDir, fileName)
	if err := os.WriteFile(filePath, decoded, 0644); err != nil {
		return "", fmt.Errorf("写入头像文件失败: %w", err)
	}
	fmt.Printf("[Avatar] 已保存: %s (%d bytes)\n", fileName, len(decoded))
	return "/avatar/" + fileName, nil
}

// resolveImagePath 先查内存，再回退 SQLite image_cache，返回图片文件路径（空串表示未找到）
func (a *App) resolveImagePath(imageID string) string {
	a.mu.RLock()
	entry, ok := a.images[imageID]
	a.mu.RUnlock()
	if ok {
		return entry.Path
	}
	if a.imageDB != nil {
		// ★ 优先查 image_cache（扫描全集）；退化时再查 images（搜索索引子集）
		if e, err := a.imageDB.GetImageEntry(imageID); err == nil && e != nil {
			return e.Path
		}
		return a.imageDB.GetImagePath(imageID)
	}
	return ""
}
func (a *App) GetThumbBaseURL2() string       { return a.thumbBaseURL2 }
func (a *App) GetImageBaseURL() string        { return a.imageBaseURL }
func (a *App) SetContext(ctx context.Context) { a.ctx = ctx }
func (a *App) GetAppVersion() string          { return "1.0.0-wails" }

func (a *App) SelectFolder() (string, error) {
	return wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{Title: "选择图片文件夹"})
}

// SaveFile 弹出原生保存对话框并将数据写入所选路径
func (a *App) SaveFile(defaultName string, content string) (string, error) {
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		DefaultFilename: defaultName,
		Title:           "导出数据",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "JSON 文件 (*.json)", Pattern: "*.json"},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("保存对话框失败: %w", err)
	}
	if path == "" {
		return "", fmt.Errorf("已取消")
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}
	return path, nil
}

func (a *App) OpenFileLocation(filePath string) error {
	if filePath == "" {
		return fmt.Errorf("路径不能为空")
	}
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("文件不存在: %s", filePath)
	}
	switch goruntime.GOOS {
	case "windows":
		return exec.Command("explorer", "/select,", filePath).Start()
	case "darwin":
		return exec.Command("open", "-R", filePath).Start()
	case "linux":
		return exec.Command("nautilus", "--select", filePath).Start()
	default:
		return fmt.Errorf("不支持的操作系统: %s", goruntime.GOOS)
	}
}

// PickIconFile 打开文件选择器，选择 SVG/ICO 图标文件，返回 base64 data URL
func (a *App) PickIconFile() (string, error) {
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择图标文件",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "图标文件 (*.svg;*.ico)", Pattern: "*.svg;*.ico"},
			{DisplayName: "SVG 文件 (*.svg)", Pattern: "*.svg"},
			{DisplayName: "ICO 文件 (*.ico)", Pattern: "*.ico"},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("打开文件对话框失败: %w", err)
	}
	if path == "" {
		return "", fmt.Errorf("已取消")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取文件失败: %w", err)
	}

	// 限制文件大小 (最大256KB)
	if len(data) > 256*1024 {
		return "", fmt.Errorf("图标文件过大，请选择小于256KB的文件")
	}

	ext := strings.ToLower(filepath.Ext(path))
	var mime string
	switch ext {
	case ".svg":
		// 确保SVG文本不含脚本，简单过滤
		mime = "data:image/svg+xml;base64,"
	case ".ico":
		mime = "data:image/x-icon;base64,"
	default:
		return "", fmt.Errorf("不支持的图标格式: %s，仅支持 SVG 和 ICO", ext)
	}

	result := mime + base64.StdEncoding.EncodeToString(data)
	return result, nil
}

func (a *App) ScanFolder(path, folderType string) *ScanResult {
	return a.ScanFolderQuick(path, folderType, false)
}

// ScanFolderQuick 快速导入：只统计文件数量并注册，不执行完整扫描
// quick=true 时只统计数量，quick=false 时执行完整扫描
func (a *App) ScanFolderQuick(path, folderType string, quick bool) *ScanResult {
	folderPath := strings.TrimSpace(path)
	if folderPath == "" {
		return &ScanResult{Success: false, Message: "请提供文件夹路径"}
	}
	if folderType == "" {
		folderType = "ai"
	}
	resolvedPath, err := filepath.Abs(folderPath)
	if err != nil {
		return &ScanResult{Success: false, Message: "路径解析失败: " + err.Error()}
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return &ScanResult{Success: false, Message: fmt.Sprintf("路径不存在: %s", resolvedPath)}
	}
	if !info.IsDir() {
		return &ScanResult{Success: false, Message: fmt.Sprintf("不是有效的目录: %s", resolvedPath)}
	}
	normalizedNew := strings.ToLower(strings.ReplaceAll(resolvedPath, "\\", "/"))
	a.mu.RLock()
	for root := range a.registeredRoots {
		if strings.ToLower(strings.ReplaceAll(root, "\\", "/")) == normalizedNew {
			a.mu.RUnlock()
			return &ScanResult{Success: false, Message: "该目录已在扫描列表中"}
		}
	}
	a.mu.RUnlock()
	a.mu.Lock()
	a.registeredRoots[resolvedPath] = true
	a.folderTypes[resolvedPath] = folderType
	a.mu.Unlock()
	go a.saveRegisteredRoots()

	// 快速模式：立刻返回，后台 goroutine 扫描
	if quick {
		rootName := filepath.Base(resolvedPath)
		// ★ 发射 folder:importing 事件，前端乐观占位
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "folder:importing", map[string]interface{}{
				"rootPath":   resolvedPath,
				"folderType": folderType,
				"rootName":   rootName,
			})
		}

		go func() {
			imageCount := a.countFilesQuick(resolvedPath)
			normalizedRoot := strings.ReplaceAll(resolvedPath, "\\", "/")
			a.mu.Lock()
			a.folderCount[normalizedRoot] = imageCount
			a.mu.Unlock()
			fmt.Printf("[快速导入] %s: 统计到 %d 个文件\n", resolvedPath, imageCount)

			a.saveRegisteredRoots()

			if a.ctx != nil {
				wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
					"rootPath": resolvedPath,
					"count":    imageCount,
				})
			}
			go a.saveImageIndexForRoot(resolvedPath)
			fmt.Printf("[快速导入] 已保存数据到磁盘\n")
		}()

		return &ScanResult{
			Success:    true,
			FolderPath: resolvedPath,
			Message:    fmt.Sprintf("正在添加文件夹: %s", rootName),
		}
	}

	// 完整扫描模式（原有逻辑）
	go a.scanRootAsync(resolvedPath)

	// 通知前端文件夹已添加，开始扫描
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "folder:added", map[string]interface{}{
			"rootPath": resolvedPath,
			"folderType": folderType,
		})
	}

	rootName := filepath.Base(resolvedPath)
	folder := &FolderNode{
		Name:       rootName,
		Path:       resolvedPath,
		ImageCount: 0,
		Children:   nil,
	}

	return &ScanResult{
		Success:    true,
		FolderPath: resolvedPath,
		Folder:     folder,
		Message:    fmt.Sprintf("已添加文件夹: %s", rootName),
	}
}

// countFilesQuick 快速统计文件夹中的图片文件数量（同时建立 folderIndex、images 和 folderCount）
// 使用 os.ReadDir 递归替代 filepath.Walk，对机械硬盘更友好（批量读取目录条目）
func (a *App) countFilesQuick(rootPath string) int {
	normalizedRoot := strings.ReplaceAll(rootPath, "\\", "/")

	folderCounts := make(map[string]int)
	localFolderIndex := make(map[string][]string)
	localImages := make(map[string]*ImageEntry)
	count := 0

	a.walkDirCount(rootPath, rootPath, normalizedRoot, "", folderCounts, localFolderIndex, localImages, &count)

	// Walk 完成后一次性写入全局 map
	a.mu.Lock()
	for fp, ids := range localFolderIndex {
		a.folderIndex[fp] = append(a.folderIndex[fp], ids...)
	}
	for id, entry := range localImages {
		if _, exists := a.images[id]; !exists {
			a.images[id] = entry
		}
	}
	a.folderCount[normalizedRoot] = count
	for folderPath, cnt := range folderCounts {
		if _, exists := a.folderCount[folderPath]; !exists {
			a.folderCount[folderPath] = cnt
		}
	}
	a.mu.Unlock()

	return count
}

// walkDirCount 递归遍历目录，使用 os.ReadDir 批量读取（对机械硬盘更友好）
func (a *App) walkDirCount(dirPath, rootPath, normalizedRoot, relDir string,
	folderCounts map[string]int,
	localFolderIndex map[string][]string,
	localImages map[string]*ImageEntry, count *int) {

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return
	}

	for _, entry := range entries {
		fullPath := filepath.Join(dirPath, entry.Name())
		if entry.IsDir() {
			subRelDir := entry.Name()
			if relDir != "" {
				subRelDir = relDir + "/" + entry.Name()
			}
			a.walkDirCount(fullPath, rootPath, normalizedRoot, subRelDir, folderCounts, localFolderIndex, localImages, count)
			continue
		}

		info, err := entry.Info()
		if err != nil || !isImageFile(fullPath) {
			continue
		}

		*count++
		stableID := generateStableID(fullPath, info.Size(), info.ModTime().UnixMilli())

		// 统计文件夹图片数量
		folderPath := normalizedRoot
		if relDir != "" {
			folderPath = normalizedRoot + "/" + relDir
		}
		folderCounts[folderPath]++

		localFolderIndex[folderPath] = append(localFolderIndex[folderPath], stableID)

		localImages[stableID] = &ImageEntry{
			ID:           stableID,
			Path:         fullPath,
			Name:         entry.Name(),
			Size:         info.Size(),
			LastModified: info.ModTime().UnixMilli(),
			Folder:       relDir,
			RootPath:     rootPath,
		}
	}

	// 为子文件夹也保证 folderIndex 有 entry
	if relDir != "" {
		folderPath := normalizedRoot + "/" + relDir
		if _, ok := localFolderIndex[folderPath]; !ok {
			localFolderIndex[folderPath] = nil
		}
	}
}

func (a *App) RemoveFolder(path string) *ScanResult {
	folderPath := strings.TrimSpace(path)
	if folderPath == "" {
		return &ScanResult{Success: false, Message: "请提供文件夹路径"}
	}
	resolvedPath, _ := filepath.Abs(folderPath)
	var matchedPath string
	a.mu.RLock()
	if a.registeredRoots[resolvedPath] {
		matchedPath = resolvedPath
	} else {
		normalizedInput := strings.ToLower(strings.ReplaceAll(folderPath, "\\", "/"))
		for root := range a.registeredRoots {
			normalizedRoot := strings.ToLower(strings.ReplaceAll(root, "\\", "/"))
			if normalizedRoot == normalizedInput || strings.HasSuffix(normalizedRoot, normalizedInput) || strings.HasSuffix(normalizedInput, normalizedRoot) {
				matchedPath = root
				break
			}
		}
	}
	a.mu.RUnlock()
	if matchedPath == "" {
		return &ScanResult{Success: false, Message: "该目录不在扫描列表中"}
	}
	a.mu.Lock()
	delete(a.registeredRoots, matchedPath)
	delete(a.folderTypes, matchedPath)
	if a.userDataDB != nil {
		a.userDataDB.RemoveRoot(matchedPath)
	}
	a.mu.Unlock()
	go a.saveRegisteredRoots()
	// 异步清理内存和数据库，避免大量文件时阻塞 UI
	go a.removeByRoot(matchedPath)
	return &ScanResult{Success: true, Message: fmt.Sprintf("已移除目录: %s", matchedPath)}
}

// RemoveImages 批量删除图片（从内存、缩略图缓存、数据库中移除）
func (a *App) RemoveImages(ids []string) map[string]interface{} {
	if len(ids) == 0 {
		return map[string]interface{}{"success": true, "removed": 0}
	}

	a.mu.Lock()
	for _, id := range ids {
		delete(a.images, id)
	}
	// 清理 folderIndex 中的无效 ID
	for folderKey, folderIDs := range a.folderIndex {
		var remaining []string
		for _, id := range folderIDs {
			if _, exists := a.images[id]; exists {
				remaining = append(remaining, id)
			}
		}
		if len(remaining) == 0 {
			delete(a.folderIndex, folderKey)
		} else {
			a.folderIndex[folderKey] = remaining
		}
	}
	a.mu.Unlock()

	// 删除缩略图
	a.removeThumbsByIDs(ids)

	// 删除数据库中记录
	if a.imageDB != nil {
		if err := a.imageDB.DeleteImagesBatch(ids); err != nil {
			fmt.Printf("[RemoveImages] 数据库删除失败: %v\n", err)
		}
	}

	a.saveImageIndex()
	fmt.Printf("[RemoveImages] 已删除 %d 张图片\n", len(ids))
	return map[string]interface{}{"success": true, "removed": len(ids)}
}

func (a *App) Refresh() (result *ScanResult) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := goruntime.Stack(buf, false)
			fmt.Printf("[PANIC] Refresh: %v\n%s\n", r, buf[:n])
			result = &ScanResult{Success: false, Message: fmt.Sprintf("内部错误: %v", r)}
		}
	}()
	a.loadUserData()
	count := a.scanAllFolders()
	a.saveImageIndex()
	return &ScanResult{Success: true, FileCount: count, Message: fmt.Sprintf("已重新扫描磁盘，共 %d 张图片", count)}
}

// DebugScanRoot 诊断扫描：对比磁盘实际文件数与应用扫描数，定位图片丢失环节。
// 可在浏览器控制台中调用：window.go.main.App.DebugScanRoot("D:\\your\\path")
func (a *App) DebugScanRoot(rootPath string) *DebugScanResult {
	resolved, err := filepath.Abs(rootPath)
	if err != nil {
		return &DebugScanResult{RootPath: rootPath}
	}
	res := &DebugScanResult{RootPath: resolved}

	// 第 1 步：raw filepath.Walk 遍历磁盘，统计实际文件数
	diskImageSet := make(map[string]bool)
	filepath.Walk(resolved, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			res.SkippedDirs = append(res.SkippedDirs, p)
			return filepath.SkipDir
		}
		if fi.IsDir() {
			return nil
		}
		res.DiskTotalFiles++
		if isImageFile(fi.Name()) || isVideoFile(fi.Name()) {
			res.DiskImageFiles++
			diskImageSet[p] = true
		}
		return nil
	})

	// 第 2 步：用应用的 scanWalk 扫描同一目录
	localImages := make(map[string]*ImageEntry)
	localFolderIndex := make(map[string][]string)
	scanRes := a.scanWalk(resolved, localImages, localFolderIndex, false)
	res.AppScanCount = scanRes.count

	// 第 3 步：找出磁盘有但应用没扫到的文件
	for p := range diskImageSet {
		found := false
		for _, entry := range scanRes.images {
			if entry.Path == p {
				found = true
				break
			}
		}
		if !found {
			if len(res.SampleMissing) < 50 {
				res.SampleMissing = append(res.SampleMissing, p)
			}
		}
	}

	// 第 4 步：检查扫描过程中跳过/失败的文件（通过 scanWalk 的日志无法捕获，这里做二次验证）
	for _, entry := range scanRes.images {
		if _, err := os.Stat(entry.Path); err != nil {
			res.FailedFiles = append(res.FailedFiles, entry.Path)
		}
	}

	fmt.Printf("[诊断] %s: 磁盘全部文件=%d, 磁盘图片=%d, 应用扫描=%d, 缺失=%d\n",
		resolved, res.DiskTotalFiles, res.DiskImageFiles, res.AppScanCount, len(res.SampleMissing))
	return res
}

// DebugAppState 诊断内部状态，输出到终端 stdout，同时返回数据给前端。
func (a *App) DebugAppState() map[string]interface{} {
	a.mu.RLock()
	defer a.mu.RUnlock()

	fmt.Println("")
	fmt.Println("========== 诊断：应用内部状态 ==========")
	fmt.Printf("已注册根目录: %d\n", len(a.registeredRoots))
	fmt.Printf("images 总数:   %d\n", len(a.images))
	fmt.Printf("folderIndex 键数: %d\n", len(a.folderIndex))
	fmt.Printf("folderCount 键数: %d\n", len(a.folderCount))
	fmt.Println("")

	rootsData := make([]map[string]interface{}, 0, len(a.registeredRoots))
	for rp := range a.registeredRoots {
		norm := strings.ReplaceAll(rp, "\\", "/")
		fc := a.folderCount[norm]

		imgCount := 0
		for _, entry := range a.images {
			if entry.RootPath == rp {
				imgCount++
			}
		}

		idxCount := 0
		seen := make(map[string]bool)
		for folderKey, ids := range a.folderIndex {
			if folderKey == norm || strings.HasPrefix(folderKey, norm+"/") {
				for _, id := range ids {
					if !seen[id] {
						seen[id] = true
						idxCount++
					}
				}
			}
		}

		diskCount := 0
		filepath.Walk(rp, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return filepath.SkipDir
			}
			if !fi.IsDir() && (isImageFile(fi.Name()) || isVideoFile(fi.Name())) {
				diskCount++
			}
			return nil
		})

		fmt.Printf("[%s]\n", filepath.Base(rp))
		fmt.Printf("  路径:              %s\n", rp)
		fmt.Printf("  folderCount:       %d\n", fc)
		fmt.Printf("  images(内存):      %d\n", imgCount)
		fmt.Printf("  folderIndex(去重): %d\n", idxCount)
		fmt.Printf("  磁盘实际图片数:    %d", diskCount)
		if diskCount != imgCount {
			fmt.Printf("  ← 不匹配！差 %d 张", diskCount-imgCount)
		}
		fmt.Println("")

		rootsData = append(rootsData, map[string]interface{}{
			"path":           rp,
			"folderCount":    fc,
			"imagesCount":    imgCount,
			"folderIndexCount": idxCount,
			"diskCount":      diskCount,
		})
	}
	fmt.Println("========================================")
	fmt.Println("")

	// 子文件夹诊断：抽查 folderIndex 中的前 5 个文件夹，对比 folderCount 与磁盘实际数
	fmt.Println("--- 子文件夹抽样对比 (folderIndex 前5个) ---")
	var firstRoot string
	for r := range a.registeredRoots {
		firstRoot = r
		break
	}
	normRoot := strings.ReplaceAll(firstRoot, "\\", "/")
	checked := 0
	for folderKey := range a.folderIndex {
		if checked >= 5 {
			break
		}
		diskSubCount := 0
		// folderKey 格式: "K:/Child Modeling Agency/sub1/sub2"
		// 去掉 rootNorm 前缀，拼接 firstRoot 得到磁盘路径
		if strings.HasPrefix(folderKey, normRoot+"/") {
			relPart := folderKey[len(normRoot)+1:]
			diskPath := filepath.Join(firstRoot, filepath.FromSlash(relPart))
			filepath.Walk(diskPath, func(p string, fi os.FileInfo, err error) error {
				if err != nil {
					return filepath.SkipDir
				}
				if !fi.IsDir() && (isImageFile(fi.Name()) || isVideoFile(fi.Name())) {
					diskSubCount++
				}
				return nil
			})
		}
		fc := a.folderCount[folderKey]
		match := "OK"
		if fc != diskSubCount {
			match = fmt.Sprintf("MISMATCH diff=%d", diskSubCount-fc)
		}
		fmt.Printf("  [%s] folderCount=%d  disk=%d  %s\n", folderKey, fc, diskSubCount, match)
		checked++
	}

	return map[string]interface{}{
		"totalImages":    len(a.images),
		"totalRoots":     len(a.registeredRoots),
		"roots":          rootsData,
	}
}

// DebugPagination 诊断分页：对指定文件夹模拟 GetImages 逻辑，报告每一步的计数。
// 使用: window.go.main.App.DebugPagination("K:\\Child Modeling Agency\\Olesya")
func (a *App) DebugPagination(folderPath string) map[string]interface{} {
	normalizedFolder := strings.ReplaceAll(folderPath, "\\", "/")

	a.mu.RLock()
	defer a.mu.RUnlock()

	// 模拟 GetImages 的匹配逻辑
	matchedKeys := 0
	totalIDs := 0
	uniqueIDs := 0
	seen := make(map[string]bool)
	var sampleKeys []string

	for folderKey, ids := range a.folderIndex {
		if folderKey == normalizedFolder || strings.HasPrefix(folderKey, normalizedFolder+"/") {
			matchedKeys++
			totalIDs += len(ids)
			for _, id := range ids {
				if !seen[id] {
					seen[id] = true
					uniqueIDs++
				}
			}
			if len(sampleKeys) < 10 {
				sampleKeys = append(sampleKeys, fmt.Sprintf("%s (%d ids)", folderKey, len(ids)))
			}
		}
	}

	// folderCount 值
	fc := a.folderCount[normalizedFolder]

	fmt.Println("")
	fmt.Println("========== 分页诊断 ==========")
	fmt.Printf("请求文件夹: %s\n", folderPath)
	fmt.Printf("规范化:     %s\n", normalizedFolder)
	fmt.Printf("匹配到的 folderIndex 键数: %d\n", matchedKeys)
	fmt.Printf("匹配键样例: %v\n", sampleKeys)
	fmt.Printf("ID 总数(含重复): %d\n", totalIDs)
	fmt.Printf("去重后 ID 数:    %d\n", uniqueIDs)
	fmt.Printf("folderCount 值:  %d\n", fc)
	fmt.Printf("images 中存在:   %d\n", len(seen)) // seen has unique IDs that exist in folderIndex
	if uniqueIDs != fc {
		fmt.Printf("*** 不一致！GetImages total=%d, folderCount=%d, 差=%d ***\n", uniqueIDs, fc, fc-uniqueIDs)
	}
	fmt.Println("===============================")
	fmt.Println("")

	return map[string]interface{}{
		"folderPath":      folderPath,
		"normalizedFolder": normalizedFolder,
		"matchedKeys":     matchedKeys,
		"totalIDs":        totalIDs,
		"uniqueIDs":       uniqueIDs,
		"folderCount":     fc,
		"sampleKeys":      sampleKeys,
	}
}

// RefreshFolder 增量刷新指定文件夹：只对比增减的文件，不重处理已存在的图片。
// folderPath 是文件系统路径（如 D:\AIImages\sub）。
// 通过 stableID（path+size+mtime 的 MD5）检测变化，避免不必要的 IO。
func (a *App) RefreshFolder(folderPath string) *FolderDiffResult {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := goruntime.Stack(buf, false)
			fmt.Printf("[PANIC] RefreshFolder: %v\n%s\n", r, buf[:n])
		}
	}()
	return a.refreshFolderInternal(folderPath)
}

func (a *App) RescanFolder(rootPath string) (result *ScanResult) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := goruntime.Stack(buf, false)
			fmt.Printf("[PANIC] RescanFolder: %v\n%s\n", r, buf[:n])
			result = &ScanResult{Success: false, Message: fmt.Sprintf("内部错误: %v", r)}
		}
	}()
	normalizedPath := strings.TrimSpace(rootPath)
	if normalizedPath == "" {
		return &ScanResult{Success: false, Message: "请提供文件夹路径"}
	}
	diff := a.refreshFolderInternal(normalizedPath)
	if diff == nil || !diff.Success {
		msg := "刷新失败"
		if diff != nil && diff.Error != "" {
			msg = diff.Error
		}
		return &ScanResult{Success: false, Message: msg}
	}
	total := len(diff.Added) + diff.Unchanged
	return &ScanResult{Success: true, FileCount: total, Message: fmt.Sprintf("已刷新: 新增 %d，移除 %d，未变 %d", len(diff.Added), len(diff.Removed), diff.Unchanged)}
}

// FullRescanFolder 全量重新扫描：先清空该文件夹的所有数据，再重新导入
func (a *App) FullRescanFolder(rootPath string) (result *ScanResult) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := goruntime.Stack(buf, false)
			fmt.Printf("[PANIC] FullRescanFolder: %v\n%s\n", r, buf[:n])
			result = &ScanResult{Success: false, Message: fmt.Sprintf("内部错误: %v", r)}
		}
	}()
	normalizedPath := strings.TrimSpace(rootPath)
	if normalizedPath == "" {
		return &ScanResult{Success: false, Message: "请提供文件夹路径"}
	}
	if !a.registeredRoots[normalizedPath] {
		return &ScanResult{Success: false, Message: "该文件夹未注册为根目录"}
	}
	// 清空内存和数据库
	a.removeByRoot(normalizedPath)
	// 全量重新扫描
	go a.scanRootAsync(normalizedPath)
	return &ScanResult{Success: true, FileCount: 0, Message: "已开始全量重新扫描"}
}

func (a *App) GetImages(folder string, offset int, limit int, sortOrder string) *ImageListResult {
	// 安全上限：limit 为 0 或超过 500 时，默认截断到 500，防止一次性返回全部数据
	if limit <= 0 || limit > 500 {
		limit = 500
	}

	// 先做轻量检查（不持锁）：folderIndex 是否为空
	a.mu.RLock()
	folderIndexLen := len(a.folderIndex)
	registeredRootsLen := len(a.registeredRoots)
	a.mu.RUnlock()

	// 自修复：folderIndex 为空但有已注册目录时，触发后台扫描
	if folderIndexLen == 0 && registeredRootsLen > 0 {
		fmt.Println("[自修复] folderIndex 为空但有注册目录，触发后台全量扫描")
		go a.scanAllFolders()
		return &ImageListResult{Items: []SafeImage{}, Total: 0, Offset: offset, Limit: limit}
	}

	var results []*ImageEntry
	var total int

	if folder != "" {
		normalizedFolder := strings.ReplaceAll(folder, "\\", "/")

		// 收集匹配的 folderKey（未加载的需 ensure）
		a.mu.RLock()
		var matchingKeys []string
		for folderKey := range a.folderIndex {
			if folderKey == normalizedFolder || strings.HasPrefix(folderKey, normalizedFolder+"/") {
				matchingKeys = append(matchingKeys, folderKey)
			}
		}
		a.mu.RUnlock()

		// 对未加载的 folderKey 触发按需加载（ensureFolderLoaded 自管锁）
		for _, fk := range matchingKeys {
			a.ensureFolderLoaded(fk)
		}

		// 自修复：无匹配 folderKey 且根目录未扫描时，触发扫描
		if len(matchingKeys) == 0 && folderIndexLen == 0 {
			needsScan := false
			a.mu.RLock()
			for root := range a.registeredRoots {
				rootNorm := strings.ReplaceAll(root, "\\", "/")
				if normalizedFolder == rootNorm || strings.HasPrefix(normalizedFolder, rootNorm+"/") {
					if _, ok := a.folderCount[rootNorm]; !ok || a.folderCount[rootNorm] == 0 {
						needsScan = true
					}
					break
				}
			}
			a.mu.RUnlock()
			if needsScan {
				fmt.Println("[自修复] 文件夹无结果但根目录未扫描，触发异步扫描")
				go a.scanRootAsync(folder)
				return &ImageListResult{Items: []SafeImage{}, Total: 0, Offset: offset, Limit: limit}
			}
		}

		// 遍历收集结果
		a.mu.RLock()
		seen := make(map[string]bool)
		for _, fk := range matchingKeys {
			ids := a.folderIndex[fk]
			for _, id := range ids {
				if seen[id] {
					continue
				}
				seen[id] = true
				if entry, ok := a.images[id]; ok {
					results = append(results, entry)
				}
			}
		}
		a.mu.RUnlock()

		fmt.Printf("[GetImages] folder=%q results=%d\n", normalizedFolder, len(results))
		if len(results) == 0 {
			sampleKeys := make([]string, 0, 5)
			a.mu.RLock()
			for k := range a.folderIndex {
				sampleKeys = append(sampleKeys, k)
				if len(sampleKeys) >= 5 {
					break
				}
			}
			a.mu.RUnlock()
			fmt.Printf("[GetImages] 查询无结果: normalizedFolder=%q folderIndexSamples=%v\n",
				normalizedFolder, sampleKeys)
		}

		total = len(results)
		// 排序
		sortImageEntries(results, sortOrder)
		// 分页
		if offset > len(results) {
			offset = len(results)
		}
		end := len(results)
		if limit > 0 && offset+limit < end {
			end = offset + limit
		}
		paged := results[offset:end]

		safe := make([]SafeImage, len(paged))
		for i, entry := range paged {
			w, h := entry.Width, entry.Height
			if !entry.IsVideo && (w == 0 || h == 0) {
				w2, h2 := getImageDimensions(entry.Path)
				if w2 > 0 && h2 > 0 {
					w, h = w2, h2
				} else {
					w, h = 400, 300
				}
			}
			safe[i] = SafeImage{
				ID:           entry.ID,
				Name:         entry.Name,
				Path:         entry.Path,
				Size:         entry.Size,
				LastModified: entry.LastModified,
				CreatedAt:    entry.CreatedAt,
				Folder:       entry.Folder,
				RootPath:     entry.RootPath,
				URL:          entry.URL,
				ThumbURL:     fmt.Sprintf("/thumb/%s", entry.ID),
				Width:        w,
				Height:       h,
				IsVideo:      entry.IsVideo,
			}
		}
		return &ImageListResult{Items: safe, Total: total, Offset: offset, Limit: limit}
	}

	// folder == ""：SQL 分页加载（不再全量遍历 a.images）
	if a.imageDB != nil {
		entries, err := a.imageDB.LoadImageCachePaged(offset, limit, sortOrder)
		if err != nil {
			fmt.Printf("[GetImages] SQL 分页失败 %v\n", err)
			return &ImageListResult{Items: []SafeImage{}, Total: 0, Offset: offset, Limit: limit}
		}
		count, _ := a.imageDB.CountImageCache()
		total = count
		safe := make([]SafeImage, len(entries))
		for i, e := range entries {
			entry := toImageEntry(e)
			w, h := entry.Width, entry.Height
			if !entry.IsVideo && (w == 0 || h == 0) {
				w2, h2 := getImageDimensions(entry.Path)
				if w2 > 0 && h2 > 0 {
					w, h = w2, h2
				} else {
					w, h = 400, 300
				}
			}
			safe[i] = SafeImage{
				ID:           entry.ID,
				Name:         entry.Name,
				Path:         entry.Path,
				Size:         entry.Size,
				LastModified: entry.LastModified,
				CreatedAt:    entry.CreatedAt,
				Folder:       entry.Folder,
				RootPath:     entry.RootPath,
				URL:          entry.URL,
				ThumbURL:     fmt.Sprintf("/thumb/%s", entry.ID),
				Width:        w,
				Height:       h,
				IsVideo:      entry.IsVideo,
			}
		}
		return &ImageListResult{Items: safe, Total: total, Offset: offset, Limit: limit}
	}

	return &ImageListResult{Items: []SafeImage{}, Total: 0, Offset: offset, Limit: limit}
}

func (a *App) GetImagesByPaths(paths []string, offset int, limit int, sortOrder string) *ImageListResult {
	if len(paths) == 0 {
		return &ImageListResult{Items: []SafeImage{}, Total: 0, Offset: offset, Limit: limit}
	}
	if a.imageDB == nil {
		return &ImageListResult{Items: []SafeImage{}, Total: 0, Offset: offset, Limit: limit}
	}
	// ★ 改 SQL 查询：不再遍历 a.images
	entries, err := a.imageDB.LoadImageCacheByPaths(paths)
	if err != nil {
		fmt.Printf("[GetImagesByPaths] SQL 查询失败 %v\n", err)
		return &ImageListResult{Items: []SafeImage{}, Total: 0, Offset: offset, Limit: limit}
	}
	var results []*ImageEntry
	for _, e := range entries {
		results = append(results, toImageEntry(e))
	}
	total := len(results)

	sortImageEntries(results, sortOrder)

	if offset > len(results) {
		offset = len(results)
	}
	end := len(results)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	paged := results[offset:end]

	safe := make([]SafeImage, len(paged))
	for i, entry := range paged {
		w, h := entry.Width, entry.Height
		if !entry.IsVideo && (w == 0 || h == 0) {
			w2, h2 := getImageDimensions(entry.Path)
			if w2 > 0 && h2 > 0 {
				w, h = w2, h2
			} else {
				w, h = 400, 300
			}
		}
		safe[i] = SafeImage{
			ID:           entry.ID,
			Name:         entry.Name,
			Path:         entry.Path,
			Size:         entry.Size,
			LastModified: entry.LastModified,
			CreatedAt:    entry.CreatedAt,
			Folder:       entry.Folder,
			RootPath:     entry.RootPath,
			URL:          entry.URL,
			ThumbURL:     fmt.Sprintf("/thumb/%s", entry.ID),
			Width:        w,
			Height:       h,
			IsVideo:      entry.IsVideo,
		}
	}
	return &ImageListResult{Items: safe, Total: total, Offset: offset, Limit: limit}
}

// sortImageEntries 根据 sortOrder 排序图片列表（原地）。
func sortImageEntries(results []*ImageEntry, sortOrder string) {
	switch sortOrder {
	case "name-asc":
		sort.SliceStable(results, func(i, j int) bool {
			return results[i].Path < results[j].Path
		})
	case "name-desc":
		sort.SliceStable(results, func(i, j int) bool {
			return results[i].Path > results[j].Path
		})
	case "size-desc":
		sort.SliceStable(results, func(i, j int) bool {
			if results[i].Size != results[j].Size {
				return results[i].Size > results[j].Size
			}
			return results[i].Path < results[j].Path
		})
	case "size-asc":
		sort.SliceStable(results, func(i, j int) bool {
			if results[i].Size != results[j].Size {
				return results[i].Size < results[j].Size
			}
			return results[i].Path < results[j].Path
		})
	case "date-asc":
		sort.SliceStable(results, func(i, j int) bool {
			if results[i].LastModified != results[j].LastModified {
				return results[i].LastModified < results[j].LastModified
			}
			return results[i].Path < results[j].Path
		})
	default: // "date-desc" 或空
		sort.SliceStable(results, func(i, j int) bool {
			if results[i].LastModified != results[j].LastModified {
				return results[i].LastModified > results[j].LastModified
			}
			return results[i].Path < results[j].Path
		})
	}
}

func (a *App) GetFolders() []*FolderNode {
	// ★ 直接读内存缓存的 thumbCounts，不触发 BoltDB 全表扫描
	thumbCounts := a.getCachedThumbCounts()
	// 如果缓存未就绪，后台异步重建（首次调用不阻塞）
	if thumbCounts == nil {
		go a.PreloadThumbCounts()
		thumbCounts = make(map[string]int) // 返回空计数，不阻塞前端
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	var roots []*FolderNode
	for rootPath := range a.registeredRoots {
		rootName := filepath.Base(rootPath)
		normalizedRoot := strings.ReplaceAll(rootPath, "\\", "/")
		node := &FolderNode{
			Name:       rootName,
			Path:       rootPath,
			ImageCount: a.folderCount[normalizedRoot],
			ThumbCount: thumbCounts[normalizedRoot],
			Children:   a.buildFolderTreeFromIndex(rootPath, thumbCounts),
		}
		roots = append(roots, node)
	}
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].Name < roots[j].Name
	})
	return roots
}

// GetFolderCount 只返回单个文件夹的图片数量，不重建整棵树。
// pollScanProgress 用这个替代 GetFolders，避免每次轮询都 O(N) 构建树。
// 返回 -1 表示该路径尚未被 registeredRoots 覆盖。
func (a *App) GetFolderCount(folderPath string) int {
	normalized := strings.ReplaceAll(folderPath, "\\", "/")
	a.mu.RLock()
	defer a.mu.RUnlock()

	// 先精确匹配
	if c, ok := a.folderCount[normalized]; ok {
		return c
	}
	// 无直接记录时，检查是否属于某个已注册根目录的子路径
	for root := range a.registeredRoots {
		rootNorm := strings.ReplaceAll(root, "\\", "/")
		if strings.HasPrefix(normalized, rootNorm+"/") {
			// 子文件夹：从 folderIndex 计算
			count := 0
			seen := make(map[string]bool)
			for folderKey, ids := range a.folderIndex {
				if folderKey == normalized || strings.HasPrefix(folderKey, normalized+"/") {
					for _, id := range ids {
						if !seen[id] {
							seen[id] = true
							count++
						}
					}
				}
			}
			return count
		}
	}
	return -1
}

func (a *App) GetImageFile(imageID string) *FileData {
	a.mu.RLock()
	entry, ok := a.images[imageID]
	a.mu.RUnlock()
	if !ok {
		// LRU 未命中：回退 SQLite 单点查询
		if a.imageDB != nil {
			if e, err := a.imageDB.GetImageEntry(imageID); err == nil && e != nil {
				entry = &ImageEntry{
					ID: e.ID, Path: e.Path, Name: e.Name, Size: e.Size,
					LastModified: e.LastModified, CreatedAt: e.CreatedAt,
					Folder: e.Folder, RootPath: e.RootPath,
					Width: e.Width, Height: e.Height, IsVideo: e.IsVideo,
					URL: fmt.Sprintf("/image/%s", e.ID),
				}
			}
		}
		if entry == nil {
			return nil
		}
	}
	data, err := os.ReadFile(entry.Path)
	if err != nil {
		fmt.Printf("[GetImageFile] 读取文件失败 %s: %v\n", entry.Path, err)
		return nil
	}
	return &FileData{
		ID:       imageID,
		Name:     entry.Name,
		MimeType: getMIMEType(filepath.Ext(entry.Name)),
		Size:     int64(len(data)),
		Data:     data,
	}
}

func (a *App) GetThumbnail(imageID string) *FileData {
	jpegBytes, err := a.serveThumbnail(imageID)
	if err != nil {
		return nil
	}
	return &FileData{
		ID:       imageID,
		Name:     imageID + ".jpg",
		MimeType: "image/jpeg",
		Size:     int64(len(jpegBytes)),
		Data:     jpegBytes,
	}
}

// FileData 文件数据传输对象
type FileData struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Data     []byte `json:"data"`
}

// ==================== 用户数据持久化 ====================

func (a *App) GetAllUserData() map[string]interface{} {
	a.mu.RLock()
	defer a.mu.RUnlock()
	data := a.readUserDataFile()
	// 返回所有可跨设备同步的字段
	clean := make(map[string]interface{})
	for _, k := range []string{"settings", "apiConfigs", "tags", "favorites", "imageTags"} {
		if v, ok := data[k]; ok {
			clean[k] = v
		}
	}
	return map[string]interface{}{"success": true, "data": clean}
}

func (a *App) SaveUserData(data map[string]interface{}) map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	current := a.readUserDataFile()
	// 允许保存 settings / apiConfigs / tags / favorites / imageTags
	allowedKeys := map[string]bool{"settings": true, "apiConfigs": true, "tags": true, "favorites": true, "imageTags": true}
	for k, v := range data {
		if !allowedKeys[k] {
			continue
		}
		if k == "settings" {
			if existingSettings, ok := current["settings"].(map[string]interface{}); ok {
				if newSettings, ok := v.(map[string]interface{}); ok {
					for sk, sv := range newSettings {
						existingSettings[sk] = sv
					}
					current["settings"] = existingSettings
					continue
				}
			}
		}
		if k == "favorites" && a.userDataDB != nil {
			// ★ 同步到 SQLite：全量替换（先清再写）
			a.syncFavoritesToDB(v)
		}
		if k == "imageTags" && a.userDataDB != nil {
			// ★ 同步到 SQLite：全量替换（先清再写）
			a.syncImageTagsToDB(v)
		}
		current[k] = v
	}
	os.MkdirAll(filepath.Dir(a.userDataFile), 0755)
	if err := a.writeUserDataFile(current); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true, "message": "用户数据已保存"}
}

// syncFavoritesToDB 将前端传来的 favorites 数据同步到 SQLite
func (a *App) syncFavoritesToDB(v interface{}) {
	paths, ok := v.([]interface{})
	if !ok {
		return
	}
	existing, _ := a.userDataDB.GetAllFavorites()
	existSet := make(map[string]bool, len(existing))
	for _, p := range existing {
		existSet[p] = true
	}
	now := time.Now().Format(time.RFC3339)
	for _, item := range paths {
		p, ok := item.(string)
		if !ok {
			continue
		}
		if !existSet[p] {
			a.userDataDB.AddFavorite(p, now)
		}
	}
}

// syncImageTagsToDB 将前端传来的 imageTags 数据同步到 SQLite
func (a *App) syncImageTagsToDB(v interface{}) {
	items, ok := v.([]interface{})
	if !ok {
		return
	}
	existing, _ := a.userDataDB.GetAllImageTags()
	existSet := make(map[string]bool, len(existing))
	for _, it := range existing {
		existSet[it.ImagePath+"::"+it.TagID] = true
	}
	now := time.Now().Format(time.RFC3339)
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		imagePath, _ := m["imagePath"].(string)
		tagID, _ := m["tagId"].(string)
		if imagePath == "" || tagID == "" {
			continue
		}
		key := imagePath + "::" + tagID
		if !existSet[key] {
			a.userDataDB.AddImageTag(imagePath, tagID, now)
		}
	}
}

func (a *App) SaveRoots(roots []string) map[string]interface{} {
	a.mu.Lock()
	a.registeredRoots = make(map[string]bool, len(roots))
	for _, r := range roots {
		a.registeredRoots[r] = true
	}
	a.mu.Unlock()
	go a.saveRegisteredRoots()
	go a.scanAllFolders()
	return map[string]interface{}{"success": true}
}

// ==================== 导入根目录管理（SQLite） ====================

// GetImportedRoots 返回完整导入根目录列表（含显示名等元数据）
func (a *App) GetImportedRoots() []database.ImportedRoot {
	if a.userDataDB == nil {
		return nil
	}
	roots, err := a.userDataDB.GetAllRoots()
	if err != nil {
		fmt.Printf("[错误] 获取导入根目录失败: %v\n", err)
		return nil
	}
	if roots == nil {
		roots = []database.ImportedRoot{}
	}
	return roots
}

// SaveRootsWithMeta 保存导入根目录（含元数据）
func (a *App) SaveRootsWithMeta(roots []database.ImportedRoot) map[string]interface{} {
	a.mu.Lock()
	a.registeredRoots = make(map[string]bool, len(roots))
	a.folderTypes = make(map[string]string, len(roots))
	for _, r := range roots {
		a.registeredRoots[r.Path] = true
		if r.FolderType != "" {
			a.folderTypes[r.Path] = r.FolderType
		}
	}
	a.mu.Unlock()

	if a.userDataDB != nil {
		if err := a.userDataDB.SaveRoots(roots); err != nil {
			return map[string]interface{}{"success": false, "error": err.Error()}
		}
	}
	go a.scanAllFolders()
	return map[string]interface{}{"success": true}
}

// ==================== 侧边栏设置（SQLite） ====================

// GetSidebarSetting 读取侧边栏设置（key: sidebar_expanded, sidebar_folder_order）
func (a *App) GetSidebarSetting(key string) map[string]interface{} {
	if a.userDataDB == nil {
		return map[string]interface{}{"success": true, "value": ""}
	}
	value, err := a.userDataDB.GetSidebarSetting(key)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true, "value": value}
}

// SetSidebarSetting 写入侧边栏设置
func (a *App) SetSidebarSetting(key, value string) map[string]interface{} {
	if a.userDataDB == nil {
		return map[string]interface{}{"success": false, "error": "数据库未初始化"}
	}
	if err := a.userDataDB.SetSidebarSetting(key, value); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true}
}

// GetAllSidebarSettings 读取所有侧边栏设置
func (a *App) GetAllSidebarSettings() map[string]interface{} {
	if a.userDataDB == nil {
		return map[string]interface{}{"success": true, "settings": map[string]string{}}
	}
	settings, err := a.userDataDB.GetAllSidebarSettings()
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true, "settings": settings}
}

// ==================== 图片标签管理（SQLite） ====================

// AddImageTag 给图片添加标签
func (a *App) AddImageTag(imagePath, tagID string) map[string]interface{} {
	if a.userDataDB == nil {
		return map[string]interface{}{"success": false, "error": "数据库未初始化"}
	}
	err := a.userDataDB.AddImageTag(imagePath, tagID, time.Now().Format(time.RFC3339))
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true}
}

// RemoveImageTagsByTagID 批量删除指定标签的所有关联
func (a *App) RemoveImageTagsByTagID(tagID string) map[string]interface{} {
	if a.userDataDB == nil {
		return map[string]interface{}{"success": false, "error": "数据库未初始化"}
	}
	err := a.userDataDB.RemoveImageTagsByTagID(tagID)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true}
}

// RemoveImageTag 移除图片标签
func (a *App) RemoveImageTag(imagePath, tagID string) map[string]interface{} {
	if a.userDataDB == nil {
		return map[string]interface{}{"success": false, "error": "数据库未初始化"}
	}
	err := a.userDataDB.RemoveImageTag(imagePath, tagID)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true}
}

// GetImageTags 获取图片的所有标签关联
func (a *App) GetImageTags(imagePath string) []database.ImageTag {
	if a.userDataDB == nil {
		return nil
	}
	tags, err := a.userDataDB.GetTagsForImage(imagePath)
	if err != nil {
		fmt.Printf("[错误] 获取图片标签失败: %v\n", err)
		return nil
	}
	if tags == nil {
		tags = []database.ImageTag{}
	}
	return tags
}

// GetImagesForTagIds 获取打了指定标签的图片路径
func (a *App) GetImagesForTagIds(tagIDs []string) []string {
	if a.userDataDB == nil {
		return nil
	}
	paths, err := a.userDataDB.GetImagePathsForTags(tagIDs)
	if err != nil {
		fmt.Printf("[错误] 按标签查询图片失败: %v\n", err)
		return nil
	}
	if paths == nil {
		paths = []string{}
	}
	return paths
}

// GetAllImageTags 获取所有标签关联
func (a *App) GetAllImageTags() []database.ImageTag {
	if a.userDataDB == nil {
		return nil
	}
	tags, err := a.userDataDB.GetAllImageTags()
	if err != nil {
		fmt.Printf("[错误] 获取所有标签关联失败: %v\n", err)
		return nil
	}
	if tags == nil {
		tags = []database.ImageTag{}
	}
	return tags
}

// ImportImageTags 批量导入标签关联
func (a *App) ImportImageTags(tags []database.ImageTag) map[string]interface{} {
	if a.userDataDB == nil {
		return map[string]interface{}{"success": false, "error": "数据库未初始化"}
	}
	err := a.userDataDB.ImportImageTags(tags)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true}
}

// ==================== 收藏管理（SQLite） ====================

// ToggleFavorite 切换收藏状态，返回新的收藏状态
func (a *App) ToggleFavorite(imagePath string) map[string]interface{} {
	if a.userDataDB == nil {
		return map[string]interface{}{"success": false, "error": "数据库未初始化"}
	}
	isFav, _ := a.userDataDB.IsFavorite(imagePath)
	if isFav {
		a.userDataDB.RemoveFavorite(imagePath)
		return map[string]interface{}{"success": true, "isFavorite": false}
	}
	a.userDataDB.AddFavorite(imagePath, time.Now().Format(time.RFC3339))
	return map[string]interface{}{"success": true, "isFavorite": true}
}

// SetFavorite 设置收藏状态
func (a *App) SetFavorite(imagePath string, value bool) map[string]interface{} {
	if a.userDataDB == nil {
		return map[string]interface{}{"success": false, "error": "数据库未初始化"}
	}
	if value {
		a.userDataDB.AddFavorite(imagePath, time.Now().Format(time.RFC3339))
	} else {
		a.userDataDB.RemoveFavorite(imagePath)
	}
	return map[string]interface{}{"success": true}
}

// GetAllFavorites 获取所有收藏图片路径
func (a *App) GetAllFavorites() []string {
	if a.userDataDB == nil {
		return nil
	}
	paths, err := a.userDataDB.GetAllFavorites()
	if err != nil {
		fmt.Printf("[错误] 获取收藏列表失败: %v\n", err)
		return nil
	}
	if paths == nil {
		paths = []string{}
	}
	return paths
}

// IsFavorite 检查图片是否已收藏
func (a *App) IsFavorite(imagePath string) bool {
	if a.userDataDB == nil {
		return false
	}
	isFav, err := a.userDataDB.IsFavorite(imagePath)
	if err != nil {
		return false
	}
	return isFav
}

// ==================== 代理请求 ====================

type ProxyRequestArgs struct {
	ID        string            `json:"id"`
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	ProxyHost string            `json:"proxyHost"`
	ProxyPort int               `json:"proxyPort"`
}

func (a *App) ProxyRequest(req *ProxyRequestArgs) map[string]interface{} {
	if req.ID == "" || req.URL == "" {
		return map[string]interface{}{"success": false, "error": "缺少必要参数"}
	}
	// 取消同 ID 的旧请求
	a.proxyCancelMu.Lock()
	if cancel, ok := a.proxyCancels[req.ID]; ok {
		cancel()
		delete(a.proxyCancels, req.ID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.proxyCancels[req.ID] = cancel
	a.proxyCancelMu.Unlock()

	go func() {
		defer func() {
			a.proxyCancelMu.Lock()
			delete(a.proxyCancels, req.ID)
			a.proxyCancelMu.Unlock()
		}()

		method := req.Method
		if method == "" {
			method = "GET"
		}
		var bodyReader io.Reader
		if req.Body != "" {
			bodyReader = strings.NewReader(req.Body)
		}
		httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, bodyReader)
		if err != nil {
			a.emitProxyResult(req.ID, map[string]interface{}{"error": err.Error()})
			return
		}
		for k, v := range req.Headers {
			httpReq.Header.Set(k, v)
		}

		client := &http.Client{Timeout: 60 * time.Second}
		if req.ProxyHost != "" && req.ProxyPort > 0 {
			proxyURL, _ := url.Parse(fmt.Sprintf("http://%s:%d", req.ProxyHost, req.ProxyPort))
			client.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			a.emitProxyResult(req.ID, map[string]interface{}{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			a.emitProxyResult(req.ID, map[string]interface{}{"error": "读取响应失败: " + readErr.Error()})
			return
		}
		if len(respBody) == 0 && resp.StatusCode >= 400 {
			a.emitProxyResult(req.ID, map[string]interface{}{
				"error": fmt.Sprintf("HTTP %d: 服务不可用，请检查 API 地址和端口是否正确", resp.StatusCode),
			})
			return
		}
		a.emitProxyResult(req.ID, map[string]interface{}{
			"status":  resp.StatusCode,
			"headers": resp.Header,
			"body":    string(respBody),
		})
	}()
	return map[string]interface{}{"success": true, "message": "代理请求已发送"}
}

func (a *App) CancelProxyRequest(requestID string) {
	a.proxyCancelMu.Lock()
	defer a.proxyCancelMu.Unlock()
	if cancel, ok := a.proxyCancels[requestID]; ok {
		cancel()
		delete(a.proxyCancels, requestID)
	}
}

func (a *App) emitProxyResult(id string, result map[string]interface{}) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "proxy:result:"+id, result)
	}
}

// ==================== 窗口状态 ====================

func (a *App) SaveWindowState(width, height, x, y int, maximised bool) {
	state := WindowState{
		Width:     width,
		Height:    height,
		X:         x,
		Y:         y,
		Maximised: maximised,
	}
	data, err := json.Marshal(state)
	if err != nil {
		fmt.Printf("[窗口] 序列化窗口状态失败: %v\n", err)
		return
	}
	if err := os.WriteFile(a.windowStateFile, data, 0644); err != nil {
		fmt.Printf("[窗口] 保存窗口状态失败: %v\n", err)
	}
}

func (a *App) GetWindowState() *WindowState {
	data, err := os.ReadFile(a.windowStateFile)
	if err != nil {
		return nil
	}
	var state WindowState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	return &state
}

// ==================== Wails 生命周期 ====================

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// 恢复窗口状态
	state := a.GetWindowState()
	if state != nil {
		if state.Maximised {
			wailsruntime.WindowMaximise(ctx)
		} else if state.Width > 0 && state.Height > 0 {
			wailsruntime.WindowSetSize(ctx, state.Width, state.Height)
			wailsruntime.WindowSetPosition(ctx, state.X, state.Y)
		}
	}

	// 后台执行：补扫缺失 → 修复搜索索引（跳过增量刷新，由用户手动触发）
	go func() {
		a.ensureImageIndex()
		// a.startupIncrementalRefresh() // 禁用启动时自动扫描，由用户手动触发
		// a.repairSearchIndex()
	}()
}

func (a *App) domready(ctx context.Context) {}

func (a *App) beforeClose(ctx context.Context) bool {
	w, h := wailsruntime.WindowGetSize(ctx)
	x, y := wailsruntime.WindowGetPosition(ctx)
	maximised := wailsruntime.WindowIsMaximised(ctx)
	a.SaveWindowState(w, h, x, y, maximised)
	return false
}

func (a *App) shutdown(ctx context.Context) {
	if a.httpServer != nil {
		a.httpServer.Shutdown(context.Background())
	}
	if a.thumbServer2 != nil {
		a.thumbServer2.Shutdown(context.Background())
	}
	if a.imageServer != nil {
		a.imageServer.Shutdown(context.Background())
	}
	if a.lanServer != nil {
		a.lanServer.Shutdown(context.Background())
	}
	if a.thumbDB != nil {
		a.thumbDB.Close()
	}
	shutdownVips()
}

// ==================== BoltDB 缩略图存储 ====================

func (a *App) openThumbDB() {
	dbPath := a.GetThumbDir()
	dir := filepath.Dir(dbPath)
	os.MkdirAll(dir, 0755)
	// 崩溃后文件锁可能短暂残留，给 5s 超时
	db, err := bbolt.Open(dbPath, 0644, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		fmt.Printf("[缩略图] 无法打开 BoltDB: %v\n", err)
		return
	}
	db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("thumbs"))
		return err
	})
	a.thumbDB = db
	fmt.Printf("[缩略图] BoltDB 已打开: %s\n", dbPath)
}

// ==================== 内部工具 ====================

var debugEnabled = true

func debugEnable() { debugEnabled = true }

func (a *App) debugLog(msg string) {
	if debugEnabled {
		fmt.Println(msg)
	}
}

// GetSavedUserDataDir 读取程序目录下的 .gallery-userdir 文件获取上次保存的自定义数据目录
func GetSavedUserDataDir(defaultDataDir string) string {
	dirFile := filepath.Join(defaultDataDir, "..", ".gallery-userdir")
	data, err := os.ReadFile(dirFile)
	if err != nil {
		return ""
	}
	saved := strings.TrimSpace(string(data))
	if saved == "" {
		return ""
	}
	if info, err := os.Stat(saved); err != nil || !info.IsDir() {
		return ""
	}
	return saved
}

// LoadWindowState 从文件读取窗口状态（独立函数，供 main.go 在 App 创建前调用）
func LoadWindowState(userDataDir string) *WindowState {
	windowStateFile := filepath.Join(userDataDir, "window-state.json")
	data, err := os.ReadFile(windowStateFile)
	if err != nil {
		return nil
	}
	var state WindowState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	return &state
}
