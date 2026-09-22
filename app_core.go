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
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"local-gallery/internal/database"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"go.etcd.io/bbolt"
)

type App struct {
	ctx                context.Context
	userDataDir        string
	defaultUserDataDir string // 程序启动时的默认路径，用于重置

	// ★ M2/M3 改造：内存图片索引（a.images / folderIndex / folderCount / LRU）已删除，
	//   图片与文件夹计数全部以 SQLite（image_cache / folders 表）为唯一真相源。

	registeredRoots map[string]bool
	folderTypes     map[string]string // rootPath -> "ai" | "photo" | "mixed"

	userDataFile    string
	windowStateFile string

	instanceLockRelease func() // 单实例锁释放函数（正常退出时删除锁文件）

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
	// ★ 即席扫描去重：GetImages 点开子文件夹时同步只扫这一个文件夹，
	//   用 scanMu 保护，避免并发点同一子文件夹重复扫描。
	subfolderScanning map[string]bool
	bgPaused       atomic.Int32
	switchMu       sync.Mutex // 防止 RestartWithNewPaths 重入
	pendingUserDir string     // SetUserDataDir 设定的待切换路径，供 RestartWithNewPaths 优先读取

	// ★ 目录修改时间缓存：refresh 时跳过"自身 mtime 未变"的叶子目录（其文件都未变），
	//   把刷新从 O(所有文件) 降为 O(变化的目录)。持久化到磁盘，重启后不重扫未变部分。
	dirMtimes map[string]int64 // 规范化目录路径(正斜杠) -> 目录 mtime(毫秒)

	imageDB    *database.ImageDB
	userDataDB *database.UserDataDB
	thumbDB    *bbolt.DB // 缩略图 BoltDB 单文件存储
	thumbDBMu  sync.Mutex

	indexingRoots   map[string]context.CancelFunc
	indexingRootsMu sync.Mutex

	proxyCancels  map[string]context.CancelFunc
	proxyCancelMu sync.Mutex
	mu            sync.RWMutex

	// ★ 预览缩略图缓存：DB 兜底取到的确定性预览（避免每次 GetFolders 对未加载文件夹重复 COUNT+OFFSET 查询）
	previewCache   map[string][]FolderPreview
	previewCacheMu sync.Mutex

	// ★ 生成参数标签缓存：全库聚合查询较重（json_each 扫全表），面板打开时优先返回缓存
	paramTagsCache   []ParamTagGroup
	paramTagsCacheAt time.Time
	paramTagsMu      sync.Mutex
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
		registeredRoots:    make(map[string]bool),
		folderTypes:        make(map[string]string),
		userDataFile:       filepath.Join(userDataDir, "user-data.json"),
		windowStateFile:    filepath.Join(userDataDir, "window-state.json"),
		scanningRoots:      make(map[string]bool),
		proxyCancels:       make(map[string]context.CancelFunc),
		indexingRoots:      make(map[string]context.CancelFunc),
		previewCache:       make(map[string][]FolderPreview),
		dirMtimes:          loadDirMtimes(filepath.Join(userDataDir, dirMtimesFileName)),
	}
	os.MkdirAll(userDataDir, 0755)

	// ★ 启动时检测是否有上次保存的自定义 userDataDir，有则切换
	app.applySavedUserDataDir()

	// ★ 单实例保护：防止两个实例并发读写同一份用户数据（并发覆盖 = 数据丢失）
	if lockRelease, lockErr := acquireInstanceLock(app.userDataDir); lockErr != nil {
		// ★ 打点：只有"启动/WindowState/initVips"三行就消失的会话，多半是走到了这里
		//   （互斥体冲突 → 弹窗 → 退出），与"在窗口创建阶段崩溃"的会话区分开。
		logStartupf("NewApp: 单实例冲突（另一个实例正在运行），本进程退出")
		fmt.Printf("[单实例] %v\n", lockErr)
		showInstanceConflictMessage()
		os.Exit(1)
	} else {
		app.instanceLockRelease = lockRelease
		logStartupf("NewApp: 已获取单实例互斥体")
	}

	// 初始化 BoltDB 缩略图存储
	logStartupf("NewApp: openThumbDB 开始")
	if err := app.openThumbDB(); err != nil {
		fmt.Printf("[缩略图] %v\n", err)
	}
	logStartupf("NewApp: openThumbDB 完成")

	// 初始化 SQLite 图片元数据库
	dbPath := filepath.Join(app.userDataDir, "images.db")
	if imageDB, err := database.New(dbPath); err != nil {
		fmt.Printf("[警告] 无法打开图片元数据库: %v，搜索功能将不可用\n", err)
	} else {
		app.imageDB = imageDB
	}
	logStartupf("NewApp: images.db 完成")

	// 初始化 SQLite 用户数据库（设置数据）
	udbPath := filepath.Join(app.userDataDir, "user-data.db")
	if userDataDB, err := database.NewUserDataDB(udbPath); err != nil {
		fmt.Printf("[警告] 无法打开用户数据库: %v\n", err)
	} else {
		app.userDataDB = userDataDB
	}
	logStartupf("NewApp: user-data.db 完成")

	// ★ 打点：这 6 步合计曾观测到 2.5~5s（偶发），逐步骤计时以定位瓶颈。
	stepT0 := time.Now()
	app.loadUserData()
	logStartupf("NewApp: 步骤 loadUserData 完成（%s）", time.Since(stepT0).Round(time.Millisecond))
	stepT0 = time.Now()
	app.loadThumbSettings()     // 恢复缩略图并发数与缩放算法设置
	logStartupf("NewApp: 步骤 loadThumbSettings 完成（%s）", time.Since(stepT0).Round(time.Millisecond))
	stepT0 = time.Now()
	app.loadThumbCountsFromDisk() // ★ 冷启动恢复上次的 thumbCounts，跳过 8.6GB 缩略图库重算
	logStartupf("NewApp: 步骤 loadThumbCountsFromDisk 完成（%s）", time.Since(stepT0).Round(time.Millisecond))
	stepT0 = time.Now()
	app.migratePromptVersions() // migrate old promptVersions to SQLite
	logStartupf("NewApp: 步骤 migratePromptVersions 完成（%s）", time.Since(stepT0).Round(time.Millisecond))
	stepT0 = time.Now()
	app.migrateUserData()       // migrate registeredRoots/imageTags/favorites to SQLite
	logStartupf("NewApp: 步骤 migrateUserData 完成（%s）", time.Since(stepT0).Round(time.Millisecond))
	stepT0 = time.Now()
	// ★ M3：不再加载内存索引，文件夹树/图片列表全部按需查 SQLite
	logStartupf("NewApp: 用户数据加载完成")

	app.startHTTPServer()
	logStartupf("NewApp: HTTP 服务完成")

	// ★ 恢复 LAN 设置：勾选了"开机自动启动"则自动开启局域网服务
	app.loadLANSettings()

	return app
}

// LogStartupTiming 前端启动打点：由 JS 在关键节点调用（DOMContentLoaded / window-load / init-done），
// 追加写入 startup.log，用于定位冷启动慢的瓶颈。
func (a *App) LogStartupTiming(tag string) {
	logStartupf("前端: %s", tag)
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
	// ★ 每次请求时动态读取 a.userDataDir：热切换用户数据目录后（RestartWithNewPaths
	//   不重建 HTTP 服务器），闭包捕获的旧目录会永久提供旧目录的图标/头像
	userIconsDir := func() string { return filepath.Join(a.userDataDir, "icons") }
	os.MkdirAll(userIconsDir(), 0755)
	fmt.Printf("[Icons] 用户图标目录: %s\n", userIconsDir())

	// avatar 目录：保存裁剪后的头像图片
	userAvatarDir := func() string { return filepath.Join(a.userDataDir, "avatar") }
	os.MkdirAll(userAvatarDir(), 0755)
	fmt.Printf("[Avatar] 头像目录: %s\n", userAvatarDir())

	// 头像文件服务
	thumbMux.HandleFunc("/avatar/", func(w http.ResponseWriter, r *http.Request) {
		fileName := strings.TrimPrefix(r.URL.Path, "/avatar/")
		if fileName == "" || strings.Contains(fileName, "..") {
			http.NotFound(w, r)
			return
		}
		avatarPath := filepath.Join(userAvatarDir(), fileName)
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
		userPath := filepath.Join(userIconsDir(), iconName)
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
		// ★ 传 r.Context()：浏览器 abort（滚动丢弃）后不再继续排队生成
		jpegBytes, err := a.serveThumbnail(r.Context(), imageID)
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
			jpegBytes, err := a.serveThumbnail(r.Context(), imageID)
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

	// ★ 原图独立 origin，浏览器分配独立 6 连接池，不与缩略图排队。
	//   固定端口 19877（被占则回退随机），保证复制/保存进 HTML 标签的
	//   图片引用链接跨重启稳定（否则每次启动端口随机，保存的链接即失效）。
	imagePort := 19877
	imageListener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", imagePort))
	if err != nil {
		fmt.Printf("[HTTP] 原图端口 %d 被占用，使用随机端口\n", imagePort)
		imageListener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fmt.Println("[HTTP] 原图服务启动失败:", err)
		}
	}
	if err == nil {
		imagePort = imageListener.Addr().(*net.TCPAddr).Port
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
			// ★ 高优先级队列：大图读取在独立 goroutine 中执行，
			//   永远先于排队中的缩略图生成（LOW）被调度。
			enqueueHighWait(func() {
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

// resolveImagePath 纯 DB 查询（image_cache 为主，退化为搜索索引子集），返回图片文件路径（空串表示未找到）
func (a *App) resolveImagePath(imageID string) string {
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

// SelectThumbDBFile 打开文件选择器，选择缩略图数据库文件（thumbnails.db）。
// ★ 直接选 .db 文件而非文件夹：路径天然指向真实的缩略图库，
//   从根上避免"thumbDir 指向了错误目录导致误用空库重新生成"的问题。
// ★ 双重校验：文件名必须为 thumbnails.db，且内容必须是含 thumbs 桶的有效缩略图库。
//   不能随便选一个 .db 文件就接受（如误选 images.db / user-data.db）。
// 返回完整文件路径；用户取消时返回空串。
func (a *App) SelectThumbDBFile() (string, error) {
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择缩略图数据库文件 (thumbnails.db)",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "缩略图数据库 (*.db)", Pattern: "*.db"},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("打开文件对话框失败: %w", err)
	}
	if path == "" {
		return "", nil // 用户取消
	}

	// ★ 名称核对：必须是 thumbnails.db
	if !strings.EqualFold(filepath.Base(path), "thumbnails.db") {
		return "", fmt.Errorf("所选文件名不是 thumbnails.db，已拒绝（请选择缩略图数据库文件，而不是 images.db 等其它数据库）")
	}

	// ★ 内容核对：当前库就是它 → 直接接受；否则校验必须是含 thumbs 桶的 bbolt 库
	if path != a.GetThumbDir() {
		if _, verr := validateThumbDBFile(path); verr != nil {
			return "", verr
		}
	}
	return path, nil
}

// validateThumbDBFile 校验文件是否为有效的缩略图数据库（bbolt + thumbs 桶），
// 返回库内缩略图数量；无效时返回错误说明原因。
func validateThumbDBFile(path string) (int, error) {
	db, err := bbolt.Open(path, 0600, &bbolt.Options{ReadOnly: true, Timeout: 2 * time.Second})
	if err != nil {
		return -1, fmt.Errorf("无法打开所选文件（不是有效的数据库，或正被其它程序占用）")
	}
	defer db.Close()
	var n int
	bucketFound := false
	_ = db.View(func(tx *bbolt.Tx) error {
		if b := tx.Bucket([]byte("thumbs")); b != nil {
			bucketFound = true
			n = b.Stats().KeyN
		}
		return nil
	})
	if !bucketFound {
		return -1, fmt.Errorf("所选文件不是缩略图数据库（缺少 thumbs 数据桶，可能是 images.db 或 user-data.db）")
	}
	return n, nil
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
	// ★ 允许父子嵌套导入（乙方案）：
	//   数据层已按图片完整路径去重（image_cache 以 path 为索引、查询按路径前缀），
	//   父根与嵌套根不会产生重复图片记录；侧栏按"嵌套根折叠进父目录 + 顶层保留
	//   独立入口"展示，计数按真实目录统计。因此不再拦截父子嵌套，仅拒绝完全
	//   相同的重复路径。
	for root := range a.registeredRoots {
		normalizedRoot := strings.ToLower(strings.ReplaceAll(root, "\\", "/"))
		if normalizedRoot == normalizedNew {
			a.mu.RUnlock()
			return &ScanResult{Success: false, Message: "该目录已在扫描列表中"}
		}
	}
	a.mu.RUnlock()

	// ★ 判断是否位于某个已注册父目录内（嵌套虚拟根）。
	//   其内容由父目录扫描按完整路径持有（浅层根持有所有权），本根仅作为
	//   侧栏的"虚拟入口"，不重复自建索引，否则会把同一批文件改挂到本根下、
	//   破坏父目录计数。
	a.mu.RLock()
	isNested := false
	for root := range a.registeredRoots {
		normalizedRoot := strings.ToLower(strings.ReplaceAll(root, "\\", "/"))
		if normalizedRoot != normalizedNew && strings.HasPrefix(normalizedNew, normalizedRoot+"/") {
			isNested = true
			break
		}
	}
	a.mu.RUnlock()

	a.mu.Lock()
	a.registeredRoots[resolvedPath] = true
	a.folderTypes[resolvedPath] = folderType
	a.mu.Unlock()
	// ★ 诊断：注册根后快照
	a.mu.RLock()
	regAfter := make([]string, 0, len(a.registeredRoots))
	for root := range a.registeredRoots {
		regAfter = append(regAfter, root)
	}
	a.mu.RUnlock()
	fmt.Printf("[删除诊断] ScanFolderQuick 已注册根: %s | 当前 registeredRoots=%v\n", resolvedPath, regAfter)
	// ★ 修复：同步持久化注册（原为 go 异步）。若导入后立即退出程序，
	//   异步 goroutine 可能未完成，注册丢失 → 重启后文件夹消失。
	//   同时写入"扫描中"标记，中途退出后下次启动能识别并补扫。
	a.setScanInProgress(resolvedPath, true)
	fmt.Printf("[导入] 已注册并写入扫描标记: %s（扫描中退出后下次启动会自动补扫）\n", resolvedPath)
	a.saveRegisteredRoots()

	// ★ 嵌套虚拟根：内容已由父目录扫描持有，不重复扫描/自建索引，
	//   仅注册并在侧栏呈现（父目录内折叠入口 + 顶层独立入口）。
	if isNested {
		a.setScanInProgress(resolvedPath, false)
		n := a.dbSubtreeCount(strings.ReplaceAll(resolvedPath, "\\", "/"))
		if n < 0 {
			n = 0
		}
		fmt.Printf("[导入] 嵌套虚拟根已注册（内容由父目录持有）: %s（当前已索引 %d 张）\n", resolvedPath, n)
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "folder:importing", map[string]interface{}{
				"rootPath":   resolvedPath,
				"folderType": folderType,
				"rootName":   filepath.Base(resolvedPath),
			})
			wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
				"rootPath":   resolvedPath,
				"count":      n,
				"thumbCount": 0,
			})
		}
		return &ScanResult{
			Success:    true,
			FolderPath: resolvedPath,
			Message:    fmt.Sprintf("已添加嵌套文件夹: %s", filepath.Base(resolvedPath)),
		}
	}

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
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("[导入] 快速扫描 PANIC: %v\n%s\n", r, debug.Stack())
				}
			}()
			fmt.Printf("[导入] 快速扫描开始: %s\n", resolvedPath)
			// ★ 占住"扫描中"标记：避免与即席扫描/scanRootAsync 并发写同一批 SQLite。
			a.scanMu.Lock()
			if a.scanningRoots == nil {
				a.scanningRoots = make(map[string]bool)
			}
			a.scanningRoots[resolvedPath] = true
			a.scanMu.Unlock()
			defer func() {
				a.scanMu.Lock()
				delete(a.scanningRoots, resolvedPath)
				a.scanMu.Unlock()
			}()

			normalizedRoot := strings.ReplaceAll(resolvedPath, "\\", "/")
			// ★ M2：磁盘 walk → 每目录批次直接 upsert SQLite（SaveImageCacheBatchCounted，
			//   事务内增量维护 folders 计数），同时发 scan:batch 让前端计数实时上升。
			totalSoFar := 0
			localEntries := make(map[string]*ImageEntry)
			a.scanWalkBatched(resolvedPath, func(entries []database.ImageCacheEntry, folderRel string, count int) {
				a.mu.RLock()
				registered := a.registeredRoots[resolvedPath]
				a.mu.RUnlock()
				if !registered {
					return
				}
				a.writeScanBatchToDB(resolvedPath, entries)
				for _, e := range entries {
					localEntries[e.ID] = toImageEntry(e)
				}
				totalSoFar += count
				if a.ctx != nil && count > 0 {
					thumbCount := 0
					if thumbCounts := a.getCachedThumbCounts(); thumbCounts != nil {
						thumbCount = thumbCounts[normalizedRoot]
					}
					wailsruntime.EventsEmit(a.ctx, "scan:batch", map[string]interface{}{
						"rootPath":   resolvedPath,
						"folder":     folderRel,
						"count":      count,
						"totalSoFar": totalSoFar,
						"thumbCount": thumbCount,
					})
				}
			})
			fmt.Printf("[导入] 快速扫描 walk 完成: %s = %d 个文件\n", resolvedPath, totalSoFar)

			// ★ 扫描期间该根目录被删除了（用户在导入中点了删除）→ 丢弃本次结果。
			a.mu.RLock()
			stillRegistered := a.registeredRoots[resolvedPath]
			a.mu.RUnlock()
			if !stillRegistered {
				fmt.Printf("[导入] 根目录已被移除，丢弃本次扫描结果: %s\n", resolvedPath)
				a.setScanInProgress(resolvedPath, false)
				return
			}

			// ★ 扫描结束统一校准 folders 计数（幂等兜底，吸收并发写入造成的偏差）。
			if a.imageDB != nil {
				a.imageDB.RecomputeFolderCountsForRoot(resolvedPath, nil)
			}
			// ★ 扫描完成，清除"扫描中"标记（中断退出时该标记残留，下次启动补扫）
			a.setScanInProgress(resolvedPath, false)

			a.invalidatePreviewCache()
			a.invalidateParamTags()

			// ★ 扫描完成：自动低优预生成缺失缩略图（后台 goroutine，不阻塞 scan:complete）。
			if totalSoFar > 0 && len(localEntries) > 0 {
				preEntries := make([]*ImageEntry, 0, len(localEntries))
				for _, e := range localEntries {
					preEntries = append(preEntries, e)
				}
				go a.triggerAutoPreGen(filepath.Base(resolvedPath), preEntries)
			}

			// ★ 元数据索引（与全量扫描一致：ai 类型全量索引）
			if totalSoFar > 0 {
				ft := a.folderTypes[resolvedPath]
				if ft == "" {
					ft = "ai"
				}
				if ft != "photo" {
					go a.batchIndexImages(localEntries, ft)
				}
			}

			if a.ctx != nil {
				thumbCount := 0
				if thumbCounts := a.getCachedThumbCounts(); thumbCounts != nil {
					thumbCount = thumbCounts[normalizedRoot]
				}
				// ★ 计数以 SQLite 为唯一真相（folders 表根行的 subtree_count）
				actualCount := a.dbSubtreeCount(normalizedRoot)
				if actualCount < 0 {
					actualCount = totalSoFar
				}
				wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
					"rootPath":   resolvedPath,
					"count":      actualCount,
					"thumbCount": thumbCount,
				})
				fmt.Printf("[导入] scan:complete 已发出: %s (count=%d)\n", resolvedPath, actualCount)
			} else {
				fmt.Printf("[导入] 警告: a.ctx 为 nil，scan:complete 未发出: %s\n", resolvedPath)
			}
			fmt.Printf("[导入] 快速扫描全部完成: %s\n", resolvedPath)
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
			"rootPath":   resolvedPath,
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

func (a *App) RemoveFolder(path string) *ScanResult {
	folderPath := strings.TrimSpace(path)
	if folderPath == "" {
		return &ScanResult{Success: false, Message: "请提供文件夹路径"}
	}
	resolvedPath, _ := filepath.Abs(folderPath)
	// ★ 诊断：删除入口快照
	a.mu.RLock()
	regSnapshot := make([]string, 0, len(a.registeredRoots))
	for root := range a.registeredRoots {
		regSnapshot = append(regSnapshot, root)
	}
	a.mu.RUnlock()
	dbBefore := a.getDBRootsForDiag()
	fmt.Printf("[删除诊断] 收到删除: path=%q | resolvedPath=%q | registeredRoots=%v | SQLite roots=%v\n",
		folderPath, resolvedPath, regSnapshot, dbBefore)

	var matchedPath string
	var directMatch bool
	a.mu.RLock()
	if a.registeredRoots[resolvedPath] {
		matchedPath = resolvedPath
		directMatch = true
	} else {
		// ★ 健壮匹配：大小写/分隔符/结尾斜杠都归一化后比较，避免前端传的路径格式
		//   (如带尾斜杠、/ 与 \ 混用)与 registeredRoots 键不一致 → 匹配不到 → 删不掉，
		//   而 SaveRootsWithMeta 又会"保留"registeredRoots 里没删掉的根,导致永久删不掉。
		normalizedInput := strings.ToLower(strings.TrimRight(strings.ReplaceAll(folderPath, "\\", "/"), "/"))
		for root := range a.registeredRoots {
			normalizedRoot := strings.ToLower(strings.TrimRight(strings.ReplaceAll(root, "\\", "/"), "/"))
			if normalizedRoot == normalizedInput ||
				strings.HasSuffix(normalizedRoot, "/"+normalizedInput) ||
				strings.HasSuffix(normalizedInput, "/"+normalizedRoot) {
				matchedPath = root
				break
			}
		}
	}
	a.mu.RUnlock()
	if matchedPath == "" {
		fmt.Printf("[删除诊断] 未匹配到注册根目录 → 返回失败: path=%q | registeredRoots=%v\n", folderPath, regSnapshot)
		return &ScanResult{Success: false, Message: "该目录不在扫描列表中"}
	}
	fmt.Printf("[删除诊断] 匹配到: matchedPath=%q (精确命中=%v)\n", matchedPath, directMatch)
	a.mu.Lock()
	delete(a.registeredRoots, matchedPath)
	delete(a.folderTypes, matchedPath)
	if a.userDataDB != nil {
		a.userDataDB.RemoveRoot(matchedPath)
	}
	a.mu.Unlock()
	// ★ 诊断：删除后状态
	a.mu.RLock()
	regAfter := make([]string, 0, len(a.registeredRoots))
	for root := range a.registeredRoots {
		regAfter = append(regAfter, root)
	}
	a.mu.RUnlock()
	fmt.Printf("[删除诊断] 已删除: matchedPath=%q | registeredRoots 剩余=%v | SQLite roots 剩余=%v\n",
		matchedPath, regAfter, a.getDBRootsForDiag())
	go a.saveRegisteredRoots()
	// ★ 嵌套虚拟根：其图片由父目录扫描持有（RootPath 为父根），移除时仅注销本根，
	//   不清空父目录的数据，否则会误删父根下的整棵子树。
	if a.isRootNested(matchedPath) {
		return &ScanResult{Success: true, Message: fmt.Sprintf("已移除嵌套目录: %s（父目录中的内容保留）", matchedPath)}
	}
	// ★ 同步清理内存和数据库：确保删完返回时 SQLite 的用户数据已真正移除。
	//   否则删除→立刻重导 会读到残留数据（双倍/幽灵）；计数从 SQLite 读时也要依赖这里
	//   及时清干净。代价是大文件夹删除会稍慢（需真删几万行），前端可提示"正在删除"。
	a.removeByRoot(matchedPath)
	return &ScanResult{Success: true, Message: fmt.Sprintf("已移除目录: %s", matchedPath)}
}

// getDBRootsForDiag 读取 SQLite imported_roots 表的所有路径（诊断用）。
// 返回空切片表示 userDataDB 未初始化或读取失败。
func (a *App) getDBRootsForDiag() []string {
	if a.userDataDB == nil {
		return []string{}
	}
	roots, err := a.userDataDB.GetAllRoots()
	if err != nil {
		fmt.Printf("[删除诊断] GetAllRoots 失败: %v\n", err)
		return []string{}
	}
	paths := make([]string, 0, len(roots))
	for _, r := range roots {
		paths = append(paths, r.Path)
	}
	return paths
}

// RemoveImages 批量删除图片（从缩略图缓存、数据库中移除）
// ★ M2：不再操作内存索引，删除后按受影响根目录校准 folders 计数。
func (a *App) RemoveImages(ids []string) map[string]interface{} {
	if len(ids) == 0 {
		return map[string]interface{}{"success": true, "removed": 0}
	}

	// 收集受影响的根目录（删除后校准计数用）
	affectedRoots := make(map[string]bool)
	if a.imageDB != nil {
		for _, id := range ids {
			if e, err := a.imageDB.GetImageEntry(id); err == nil && e != nil {
				affectedRoots[e.RootPath] = true
			}
		}
	}

	// 删除缩略图
	a.removeThumbsByIDs(ids)

	// 删除数据库中记录（image_cache 全集 + 搜索索引）
	if a.imageDB != nil {
		if err := a.imageDB.DeleteImagesBatch(ids); err != nil {
			fmt.Printf("[RemoveImages] 数据库删除失败: %v\n", err)
		}
		if err := a.imageDB.DeleteImageCacheBatch(ids); err != nil {
			fmt.Printf("[RemoveImages] image_cache 删除失败: %v\n", err)
		}
		// ★ 校准受影响根目录的 folders 计数
		for root := range affectedRoots {
			a.imageDB.RecomputeFolderCountsForRoot(root, nil)
		}
	}

	a.invalidatePreviewCache()
	a.invalidateParamTags()
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

	// 第 2 步：用应用的批量扫描（与真实导入同一 walk 路径）扫描同一目录
	var scanned []*ImageEntry
	a.scanWalkBatched(resolved, func(entries []database.ImageCacheEntry, folderRel string, count int) {
		for _, e := range entries {
			scanned = append(scanned, toImageEntry(e))
		}
	})
	res.AppScanCount = len(scanned)

	// 第 3 步：找出磁盘有但应用没扫到的文件
	for p := range diskImageSet {
		found := false
		for _, entry := range scanned {
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
	for _, entry := range scanned {
		if _, err := os.Stat(entry.Path); err != nil {
			res.FailedFiles = append(res.FailedFiles, entry.Path)
		}
	}

	fmt.Printf("[诊断] %s: 磁盘全部文件=%d, 磁盘图片=%d, 应用扫描=%d, 缺失=%d\n",
		resolved, res.DiskTotalFiles, res.DiskImageFiles, res.AppScanCount, len(res.SampleMissing))
	return res
}

// DebugAppState 诊断内部状态，输出到终端 stdout，同时返回数据给前端。
// ★ M3：改用 SQLite 计数（folders 表 / image_cache），不再依赖内存索引。
func (a *App) DebugAppState() map[string]interface{} {
	a.mu.RLock()
	roots := make([]string, 0, len(a.registeredRoots))
	for rp := range a.registeredRoots {
		roots = append(roots, rp)
	}
	a.mu.RUnlock()

	fmt.Println("")
	fmt.Println("========== 诊断：应用内部状态 ==========")
	fmt.Printf("已注册根目录: %d\n", len(roots))
	fmt.Printf("image_cache 总数: %d\n", func() int {
		if a.imageDB == nil {
			return 0
		}
		n, _ := a.imageDB.CountImageCache()
		return n
	}())
	fmt.Println("")

	rootsData := make([]map[string]interface{}, 0, len(roots))
	for _, rp := range roots {
		norm := strings.ReplaceAll(rp, "\\", "/")
		fc := a.dbSubtreeCount(norm)
		if fc < 0 {
			fc = 0
		}
		dbCount := 0
		if a.imageDB != nil {
			dbCount = a.imageDB.CountByRoot(rp)
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
		fmt.Printf("  folders 表计数:    %d\n", fc)
		fmt.Printf("  image_cache 记录:  %d\n", dbCount)
		fmt.Printf("  磁盘实际图片数:    %d", diskCount)
		if diskCount != fc {
			fmt.Printf("  ← 不匹配！差 %d 张", diskCount-fc)
		}
		fmt.Println("")

		rootsData = append(rootsData, map[string]interface{}{
			"path":             rp,
			"folderCount":      fc,
			"imagesCount":      dbCount,
			"folderIndexCount": fc, // 兼容旧诊断字段（内存索引已删除，与 folders 表计数一致）
			"diskCount":        diskCount,
		})
	}
	fmt.Println("========================================")
	fmt.Println("")

	// 子文件夹抽样对比：抽查 folders 表前 5 个子文件夹，对比计数与磁盘实际数
	if a.imageDB != nil && len(roots) > 0 {
		fmt.Println("--- 子文件夹抽样对比 (folders 表前5个) ---")
		firstRoot := roots[0]
		rows, err := a.imageDB.LoadFolderTree(firstRoot)
		if err == nil {
			checked := 0
			for _, row := range rows {
				if row.Folder == "" || checked >= 5 {
					continue
				}
				diskPath := filepath.Join(firstRoot, filepath.FromSlash(row.Folder))
				diskSubCount := 0
				filepath.Walk(diskPath, func(p string, fi os.FileInfo, err error) error {
					if err != nil {
						return filepath.SkipDir
					}
					if !fi.IsDir() && (isImageFile(fi.Name()) || isVideoFile(fi.Name())) {
						diskSubCount++
					}
					return nil
				})
				match := "OK"
				if row.SubtreeCount != diskSubCount {
					match = fmt.Sprintf("MISMATCH diff=%d", diskSubCount-row.SubtreeCount)
				}
				fmt.Printf("  [%s] subtreeCount=%d  disk=%d  %s\n", row.Folder, row.SubtreeCount, diskSubCount, match)
				checked++
			}
		}
	}

	return map[string]interface{}{
		"totalImages": func() int {
			if a.imageDB == nil {
				return 0
			}
			n, _ := a.imageDB.CountImageCache()
			return n
		}(),
		"totalRoots": len(roots),
		"roots":      rootsData,
	}
}

// DebugPagination 诊断分页：对指定文件夹模拟 GetImages 逻辑，报告每一步的计数。
// 使用: window.go.main.App.DebugPagination("K:\\Child Modeling Agency\\Olesya")
// ★ M3：改用 SQLite 查询（image_cache 路径前缀计数 + folders 表计数）。
func (a *App) DebugPagination(folderPath string) map[string]interface{} {
	normalizedFolder := strings.ReplaceAll(folderPath, "\\", "/")

	uniqueIDs := 0
	if a.imageDB != nil {
		if ids, err := a.imageDB.LoadImageCacheIDsUnderPath(normalizedFolder); err == nil {
			uniqueIDs = len(ids)
		}
	}
	fc := a.dbSubtreeCount(normalizedFolder)

	fmt.Println("")
	fmt.Println("========== 分页诊断 ==========")
	fmt.Printf("请求文件夹: %s\n", folderPath)
	fmt.Printf("规范化:     %s\n", normalizedFolder)
	fmt.Printf("image_cache 子树记录数: %d\n", uniqueIDs)
	fmt.Printf("folders 表计数:  %d\n", fc)
	fmt.Printf("image_cache 中存在:   %d\n", uniqueIDs)
	if uniqueIDs != fc {
		fmt.Printf("*** 不一致！GetImages total=%d, folders 表=%d, 差=%d ***\n", uniqueIDs, fc, fc-uniqueIDs)
	}
	fmt.Println("===============================")
	fmt.Println("")

	return map[string]interface{}{
		"folderPath":       folderPath,
		"normalizedFolder": normalizedFolder,
		"matchedKeys":      0, // 兼容旧诊断字段（内存索引已删除）
		"totalIDs":         uniqueIDs,
		"uniqueIDs":        uniqueIDs,
		"folderCount":      fc,
		"sampleKeys":       []string{},
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

	// backfillImageDimensions 后台异步回填缺失的图片尺寸(getImageDimensions → UpdateImageDimensions)。
	//   GetImages 不再同步算尺寸(慢盘 ~920ms/张 × N = 卡顿)，改由这里在后台补齐，
	//   之后二次打开/瀑布流直接命中数据库里的真实宽高比。限速 30ms/张 + 上限 batchCap 张，
	//   避免长时间霸占 L 盘(慢盘)带宽、拖累缩略图 worker。
	func (a *App) backfillImageDimensions(entries []*ImageEntry) {
		const batchCap = 100 // 每次最多回填 100 张，够瀑布流前几行，也限制慢盘占用
		if len(entries) > batchCap {
			entries = entries[:batchCap]
		}
		for _, e := range entries {
			if e == nil || e.Path == "" {
				continue
			}
			w, h := getImageDimensions(e.Path)
			if w > 0 && h > 0 && a.imageDB != nil {
				a.imageDB.UpdateImageDimensions(e.ID, w, h)
			}
			// ★ fastImageDimensions 只读文件头（<1ms/张），旧实现的 30ms 限速已无必要；
			//   保留 2ms 让出调度，避免紧循环饿缩略图 worker。
			time.Sleep(2 * time.Millisecond)
		}
	}

	func (a *App) GetImages(folder string, offset int, limit int, sortOrder string) *ImageListResult {
	debugSwitchf("[GetImages] 进入: folder=%q offset=%d limit=%d sort=%q", folder, offset, limit, sortOrder)
	// 安全上限：limit 为 0 或超过 500 时，默认截断到 500，防止一次性返回全部数据
	if limit <= 0 || limit > 500 {
		limit = 500
	}

	// ★ M3：读取纯 SQL（image_cache 路径前缀分页），不再有内存回退/LRU 分支。

	if folder != "" {
		normalizedFolder := strings.ReplaceAll(folder, "\\", "/")

		// ★ 自愈：打开的文件夹在磁盘上已不存在 → 清理整棵子树并返回空，
		//   避免图廊显示一堆指向不存在文件的破图（导航栏残留的幽灵文件夹被点击时触发）。
		if info, err := os.Stat(normalizedFolder); err != nil || !info.IsDir() {
			fmt.Printf("[幽灵清理] 打开的文件夹已不存在，自动清理子树: %s\n", normalizedFolder)
			a.pruneFolderKeys([]string{normalizedFolder})
			return &ImageListResult{Items: []SafeImage{}, Total: 0, Offset: offset, Limit: limit}
		}

		// ★ 共存修复：按绝对路径前缀查询，不依赖"记录归属哪个根"。
		//   嵌套根与父根覆盖同一批文件时都能查到同一批记录（记录存的是完整路径），
		//   两者可共存浏览。
		pathPrefix := strings.ReplaceAll(normalizedFolder, "/", "\\")

	// buildItems 把 DB 条目转为 SafeImage 列表（按需尺寸 + 后台回填 + 首屏预生成）
	buildItems := func(entries []database.ImageCacheEntry, dbTotal int) *ImageListResult {
		safe := make([]SafeImage, len(entries))
		var dimBackfill []*ImageEntry
		var dimUpdates []dimUpdate
		syncBudget := dimSyncBudgetPerPage
		for i, e := range entries {
			w, h := e.Width, e.Height
			needBackfill := !e.IsVideo && (w == 0 || h == 0)
			if needBackfill && syncBudget > 0 {
				// ★ 尺寸按需优先：与缩略图一样"当前页面优先"——头部快路径 <1ms/张，
				//   首屏直接返回真实宽高，瀑布流第一屏就是正确的（不再显示占位比例）。
				if sw, sh := fastImageDimensions(e.Path); sw > 0 && sh > 0 {
					w, h = sw, sh
					needBackfill = false
					syncBudget--
					dimUpdates = append(dimUpdates, dimUpdate{id: e.ID, w: sw, h: sh})
				}
			}
			// ★ 不再伪造 400×300：快路径没赶上的（预算耗尽/解析失败）返回真 0，
			//   前端显示"未知"并用占位比例渲染 + onload 自校正，由后台回填写库兜底。
			safe[i] = SafeImage{
					ID:           e.ID,
					Name:         e.Name,
					Path:         e.Path,
					Size:         e.Size,
					LastModified: e.LastModified,
					CreatedAt:    e.CreatedAt,
					Folder:       e.Folder,
					RootPath:     e.RootPath,
					URL:          "",
					ThumbURL:     fmt.Sprintf("/thumb/%s", e.ID),
					Width:        w,
					Height:       h,
					IsVideo:      e.IsVideo,
				}
				if needBackfill {
					dimBackfill = append(dimBackfill, toImageEntry(e))
				}
			}
			// ★ 后台异步回填剩余缺失尺寸(不阻塞 GetImages)；同步补齐的先批量写库。
			a.writeDimsAsync(dimUpdates)
			if len(dimBackfill) > 0 {
				go a.backfillImageDimensions(dimBackfill)
			}
			// ★ 预生成：首次加载(offset==0)该文件夹后就让它进入后台自动预生成，
			//   缺缩略图由 worker 低优先级补齐。只对 offset==0 首次加载触发。
			if offset == 0 && len(entries) > 0 {
				preEntries := make([]*ImageEntry, 0, len(entries))
				for _, e := range entries {
					preEntries = append(preEntries, toImageEntry(e))
				}
				go a.triggerAutoPreGen(filepath.Base(normalizedFolder), preEntries)
			}
			return &ImageListResult{Items: safe, Total: dbTotal, Offset: offset, Limit: limit}
		}

		if a.imageDB != nil {
			debugSwitchf("[GetImages] 查询 SQLite: folder=%s pathPrefix=%s offset=%d limit=%d sort=%s", normalizedFolder, pathPrefix, offset, limit, sortOrder)
			tSQL0 := time.Now()
			entries, dbTotal, err := a.imageDB.LoadImageCacheByPathPrefixPaged(pathPrefix, offset, limit, sortOrder)
			debugSwitchf("[GetImages] SQLite 返回: dbTotal=%d entries=%d err=%v 耗时=%.1fms", dbTotal, len(entries), err, time.Since(tSQL0).Seconds()*1000)
			if err != nil {
				fmt.Printf("[GetImages] SQL folder pagination failed: %v\n", err)
			} else if dbTotal > 0 {
				fmt.Printf("[GetImages] SQL folder=%q offset=%d limit=%d returned=%d total=%d\n", normalizedFolder, offset, limit, len(entries), dbTotal)
				return buildItems(entries, dbTotal)
			}
			// err==nil 但 dbTotal==0：SQLite 尚无该文件夹数据（扫描进行中或未落库）。

			// ★ 即席优先扫描（仅首屏）：点开的子文件夹还没被后台大扫描扫到时，
			//   立即只扫这一个文件夹（含子树），diff 直写 DB，然后重查一次。
			//   ★ 用原始反斜杠路径(folder)做磁盘 IO，Windows 更稳；内部再做大小写不敏感匹配。
			if offset == 0 {
				if scanned := a.scanSubfolderImmediate(folder); scanned > 0 {
					entries2, dbTotal2, err2 := a.imageDB.LoadImageCacheByPathPrefixPaged(pathPrefix, offset, limit, sortOrder)
					if err2 == nil && dbTotal2 > 0 {
						fmt.Printf("[GetImages] 即席扫描后 SQL 命中: folder=%q total=%d\n", normalizedFolder, dbTotal2)
						return buildItems(entries2, dbTotal2)
					}
				}
				// ★ 自愈：目录存在但无任何记录（历史误删/中断扫描）→ 后台补扫该文件夹，
				//   完成后 scan:complete 驱动前端自动重拉，标签点击无需手动刷新即可恢复。
				a.autoHealMissingFolder(normalizedFolder)
			}
		}

		return &ImageListResult{Items: []SafeImage{}, Total: 0, Offset: offset, Limit: limit}
	}

	// folder == ""：SQL 分页加载（不再全量遍历 a.images）
	if a.imageDB != nil {
		entries, err := a.imageDB.LoadImageCachePaged(offset, limit, sortOrder)
		if err != nil {
			fmt.Printf("[GetImages] SQL 分页失败 %v\n", err)
			return &ImageListResult{Items: []SafeImage{}, Total: 0, Offset: offset, Limit: limit}
		}
		count, _ := a.imageDB.CountImageCache()
		total := count
		safe := make([]SafeImage, len(entries))
		var dimBackfill []*ImageEntry
		var dimUpdates []dimUpdate
		syncBudget := dimSyncBudgetPerPage
		for i, e := range entries {
			entry := toImageEntry(e)
			w, h := entry.Width, entry.Height
			needBackfill := !entry.IsVideo && (w == 0 || h == 0)
			if needBackfill && syncBudget > 0 {
				// ★ 尺寸按需优先（同 folder 分支）：首屏页内快路径同步补齐
				if sw, sh := fastImageDimensions(entry.Path); sw > 0 && sh > 0 {
					w, h = sw, sh
					needBackfill = false
					syncBudget--
					dimUpdates = append(dimUpdates, dimUpdate{id: entry.ID, w: sw, h: sh})
				}
			}
			if needBackfill {
				dimBackfill = append(dimBackfill, entry)
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
		a.writeDimsAsync(dimUpdates)
		if len(dimBackfill) > 0 {
			go a.backfillImageDimensions(dimBackfill)
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
	var dimBackfill []*ImageEntry
	var dimUpdates []dimUpdate
	syncBudget := dimSyncBudgetPerPage
	for i, entry := range paged {
		w, h := entry.Width, entry.Height
		needBackfill := !entry.IsVideo && (w == 0 || h == 0)
		if needBackfill && syncBudget > 0 {
			// ★ 尺寸按需优先（同 GetImages）：页内快路径同步补齐
			if sw, sh := fastImageDimensions(entry.Path); sw > 0 && sh > 0 {
				w, h = sw, sh
				needBackfill = false
				syncBudget--
				dimUpdates = append(dimUpdates, dimUpdate{id: entry.ID, w: sw, h: sh})
			}
		}
		if needBackfill {
			dimBackfill = append(dimBackfill, entry)
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
	a.writeDimsAsync(dimUpdates)
	if len(dimBackfill) > 0 {
		go a.backfillImageDimensions(dimBackfill)
	}
	return &ImageListResult{Items: safe, Total: total, Offset: offset, Limit: limit}
}

// dimUpdate 一次按需补齐的尺寸（待写库）
type dimUpdate struct {
	id string
	w  int
	h  int
}

// dimSyncBudgetPerPage 每次 GetImages 响应里同步快解析尺寸的预算（张）。
// 头部快路径 <1ms/张，300 张 ≈ 100ms 内；超出预算与快路径失败的转后台回填，
// 保证 GetImages 永不被慢路径（vips/全文件 EXIF）拖住。
const dimSyncBudgetPerPage = 300

// writeDimsAsync 批量把按需补齐的尺寸写库（异步，不阻塞响应；重复写幂等）
func (a *App) writeDimsAsync(updates []dimUpdate) {
	if len(updates) == 0 || a.imageDB == nil {
		return
	}
	go func() {
		for _, u := range updates {
			a.imageDB.UpdateImageDimensions(u.id, u.w, u.h)
		}
	}()
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
	case "folder-asc":
		sort.SliceStable(results, func(i, j int) bool {
			if results[i].Folder != results[j].Folder {
				return results[i].Folder < results[j].Folder
			}
			return results[i].Path < results[j].Path
		})
	case "folder-desc":
		sort.SliceStable(results, func(i, j int) bool {
			if results[i].Folder != results[j].Folder {
				return results[i].Folder > results[j].Folder
			}
			return results[i].Path < results[j].Path
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

// GetFolders 文件夹树（★ M3：纯 SQL——folders 表 + 预览 DB 兜底，不再构建内存索引）。
func (a *App) GetFolders() []*FolderNode {
	// ★ 直接读内存缓存的 thumbCounts，不触发 BoltDB 全表扫描
	thumbCounts := a.getCachedThumbCounts()
	// 如果缓存未就绪，延迟到启动渲染完成后异步重建（不阻塞、不抢首屏磁盘/CPU）
	if thumbCounts == nil {
		a.scheduleThumbCountPreload()
		thumbCounts = make(map[string]int) // 返回空计数，不阻塞前端
	} else if a.thumbCountsNeedRefresh() {
		// ★ 已从磁盘恢复上次计数（侧栏先按上次值显示，不再闪 0），
		//   但缩略图库有变化 → 后台重算纠正。重算在锁外进行，不影响浏览。
		a.scheduleThumbCountPreload()
	}
	// ★ 已开启"子文件夹缩略图预览"的路径集合（读 SQLite，不放锁内）
	previewRoots := a.loadPreviewRoots()
	// ★ 预取开启预览文件夹的预览数据：全部走 SQLite（带内存缓存，避免重复 COUNT+OFFSET 查询）
	previews := a.collectPreviewData(previewRoots)
	fmt.Printf("[预览] GetFolders: 开启 %d 个预览根, 预取到 %d 个文件夹的预览\n", len(previewRoots), len(previews))

	// 从 folders 表加载全部根的树（一次查询，按根分组）
	rowsByRoot := make(map[string][]database.FolderRow)
	if a.imageDB != nil {
		if allRows, err := a.imageDB.LoadAllFolderTrees(); err == nil {
			for _, row := range allRows {
				rowsByRoot[row.RootPath] = append(rowsByRoot[row.RootPath], row)
			}
		}
	}

	// 快照已注册根目录（不持锁做树构建）
	a.mu.RLock()
	rootPaths := make([]string, 0, len(a.registeredRoots))
	for rootPath := range a.registeredRoots {
		rootPaths = append(rootPaths, rootPath)
	}
	a.mu.RUnlock()

	fmt.Printf("[GetFolders] registeredRoots 数量 = %d\n", len(rootPaths))
	var roots []*FolderNode
	for _, rootPath := range rootPaths {
		rootName := filepath.Base(rootPath)
		normalizedRoot := strings.ReplaceAll(rootPath, "\\", "/")
		// ★ 计数以 folders 表为唯一真相（嵌套根记在外层父根名下）
		imageCount := a.dbSubtreeCount(normalizedRoot)
		if imageCount < 0 {
			imageCount = 0
		}
		node := &FolderNode{
			Name:       rootName,
			Path:       rootPath,
			ImageCount: imageCount,
			ThumbCount: thumbCounts[normalizedRoot],
			Children:   a.buildFolderTreeFromDB(rootPath, rowsByRoot[rootPath], thumbCounts, previews),
		}
		// ★ 根节点自身也支持预览
		if ps, ok := previews[normalizedRoot]; ok && len(ps) > 0 {
			node.Previews = ps
		}
		roots = append(roots, node)
	}
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].Name < roots[j].Name
	})
	return roots
}

// collectPreviewData 为所有命中"已开启预览"子树的文件夹取预览数据。
// ★ M3：全部走 SQLite（folderPreviewsCachedFromDB 带内存缓存），不依赖内存索引。
func (a *App) collectPreviewData(previewRoots map[string]bool) map[string][]FolderPreview {
	result := make(map[string][]FolderPreview)
	if len(previewRoots) == 0 || a.imageDB == nil {
		return result
	}
	allRows, err := a.imageDB.LoadAllFolderTrees()
	if err != nil {
		return result
	}
	for _, row := range allRows {
		rootNorm := strings.ReplaceAll(row.RootPath, "\\", "/")
		key := rootNorm
		if row.Folder != "" {
			key = rootNorm + "/" + row.Folder
		}
		if !inPreviewSubtree(key, previewRoots) {
			continue
		}
		if ps := a.folderPreviewsCachedFromDB(key, 4); len(ps) > 0 {
			result[key] = ps
		}
	}
	return result
}

// folderPreviewsCachedFromDB 带内存缓存的 DB 兜底取预览（按 folderKey 缓存，扫描/增量刷新时失效）
func (a *App) folderPreviewsCachedFromDB(folderKey string, maxN int) []FolderPreview {
	a.previewCacheMu.Lock()
	if a.previewCache == nil {
		a.previewCache = make(map[string][]FolderPreview)
	}
	if ps, ok := a.previewCache[folderKey]; ok {
		a.previewCacheMu.Unlock()
		return ps
	}
	a.previewCacheMu.Unlock()
	ps := a.folderPreviewsFromDB(folderKey, maxN)
	a.previewCacheMu.Lock()
	a.previewCache[folderKey] = ps
	a.previewCacheMu.Unlock()
	return ps
}

// invalidatePreviewCache 图片数据变化（扫描/增量刷新）时清空预览缓存
func (a *App) invalidatePreviewCache() {
	a.previewCacheMu.Lock()
	a.previewCache = make(map[string][]FolderPreview)
	a.previewCacheMu.Unlock()
}

// folderPreviewsFromDB 从 SQLite 兜底取某文件夹的确定性预览（COUNT + OFFSET，LIMIT 1 开销极低，不加载进 LRU）
// ★ 嵌套目录可能被单独注册为根（如 G:\A\B 既在 G:\A 下又是独立根），
//   图片实际存在哪个 root_path 下由数据决定——逐个候选根尝试，取第一个能查到数据的结果。
func (a *App) folderPreviewsFromDB(folderKey string, maxN int) []FolderPreview {
	if a.imageDB == nil {
		return nil
	}
	// 收集所有命中该 key 的注册根（root=原始路径, rel=相对路径）
	type cand struct{ root, rel string }
	var cands []cand
	a.mu.RLock()
	for root := range a.registeredRoots {
		rootNorm := strings.ReplaceAll(root, "\\", "/")
		if folderKey == rootNorm {
			cands = append(cands, cand{root, ""})
			continue
		}
		if strings.HasPrefix(folderKey, rootNorm+"/") {
			cands = append(cands, cand{root, folderKey[len(rootNorm)+1:]})
		}
	}
	a.mu.RUnlock()
	if len(cands) == 0 {
		return nil
	}
	// 最精确（最长）的根优先
	sort.Slice(cands, func(i, j int) bool { return len(cands[i].root) > len(cands[j].root) })
	for _, c := range cands {
		total, err := a.imageDB.CountImagesByFolder(c.root, c.rel)
		if err != nil || total <= 0 {
			continue
		}
		n := maxN
		if total < n {
			n = total
		}
		offs := previewOffsets(folderKey, total, n)
		out := make([]FolderPreview, 0, len(offs))
		for _, off := range offs {
			id, lm, p, err := a.imageDB.GetImageByFolderOffset(c.root, c.rel, off)
			if err == nil && id != "" {
				out = append(out, FolderPreview{ID: id, LastModified: lm, Path: p})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// loadPreviewRoots 从 sidebar_settings 读取"已开启子文件夹预览"的归一化路径集合
func (a *App) loadPreviewRoots() map[string]bool {
	result := make(map[string]bool)
	if a.userDataDB == nil {
		return result
	}
	value, err := a.userDataDB.GetSidebarSetting("sidebar_preview_folders")
	if err != nil || value == "" {
		return result
	}
	var paths []string
	if json.Unmarshal([]byte(value), &paths) != nil {
		return result
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = strings.ReplaceAll(p, "\\", "/")
		p = strings.TrimRight(p, "/")
		if p != "" {
			result[p] = true
		}
	}
	return result
}

// GetFolderCount 只返回单个文件夹的图片数量（含子树），不重建整棵树。
// pollScanProgress 用这个替代 GetFolders，避免每次轮询都 O(N) 构建树。
// ★ M3：直接查 folders 表（dbSubtreeCount），返回 -1 表示该路径尚未被
// registeredRoots 覆盖（语义与旧内存实现一致）。
func (a *App) GetFolderCount(folderPath string) int {
	normalized := strings.ReplaceAll(folderPath, "\\", "/")
	return a.dbSubtreeCount(normalized)
}

func (a *App) GetFolderProgress(folderPath string) map[string]int {
	normalized := strings.ReplaceAll(folderPath, "\\", "/")
	count := a.GetFolderCount(folderPath)
	thumbCount := 0
	thumbCounts := a.getCachedThumbCounts()
	if thumbCounts == nil {
		a.scheduleThumbCountPreload()
	} else {
		thumbCount = thumbCounts[normalized]
		if a.thumbCountsNeedRefresh() {
			a.scheduleThumbCountPreload()
		}
	}
	return map[string]int{"count": count, "thumbCount": thumbCount}
}

func (a *App) GetImageFile(imageID string) *FileData {
	// ★ M3：纯 DB 单点查询（image_cache 全集）
	var entry *ImageEntry
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
	// ★ 高优先级队列：大图文件读取在独立 goroutine 中执行，
	//   不会被排队中的缩略图生成（LOW）拖住。
	var result *FileData
	enqueueHighWait(func() {
		data, err := os.ReadFile(entry.Path)
		if err != nil {
			fmt.Printf("[GetImageFile] 读取文件失败 %s: %v\n", entry.Path, err)
			return
		}
		result = &FileData{
			ID:       imageID,
			Name:     entry.Name,
			MimeType: getMIMEType(filepath.Ext(entry.Name)),
			Size:     int64(len(data)),
			Data:     data,
		}
	})
	return result
}

func (a *App) GetThumbnail(imageID string) *FileData {
	// ★ 传应用上下文：程序退出时尚未开始的生成会跳过（不再空转）
	jpegBytes, err := a.serveThumbnail(a.ctx, imageID)
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
	// ★ 防误清（前端首次同步竞态的兜底）：应用启动早期若触发一次保存，
	//   前端缓存还是空的，会把 tags/apiConfigs/favorites/imageTags 整组覆盖成空。
	//   特征 = 本次请求的所有数据集合全为空（只有 settings 有值）→ 拒绝覆盖。
	raceLikeSave := allCollectionsEmpty(data)
	for k, v := range data {
		if !allowedKeys[k] {
			continue
		}
		if raceLikeSave {
			if arr, ok := v.([]interface{}); ok && len(arr) == 0 {
				if cur, ok2 := current[k].([]interface{}); ok2 && len(cur) > 0 {
					fmt.Printf("[用户数据] 检测到疑似启动竞态保存（%s 将被空数组覆盖），已拒绝\n", k)
					continue
				}
			}
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

// allCollectionsEmpty 判断本次保存请求中 tags/apiConfigs/favorites/imageTags 是否全部为空
// （启动竞态保存的特征：只有 settings 有值，其余集合全是空数组）
func allCollectionsEmpty(data map[string]interface{}) bool {
	for _, k := range []string{"tags", "apiConfigs", "favorites", "imageTags"} {
		if arr, ok := data[k].([]interface{}); ok && len(arr) > 0 {
			return false
		}
	}
	return true
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
	// ★ 修复：返回数据库里真实的 added_at（= 文件夹加入导航栏/导入的日期）。
	//   旧逻辑曾把 added_at 覆盖成文件夹的文件创建时间，导致"添加日期"排序
	//   显示的是文件夹本身的创建日期而不是导入日期。真实导入时间由
	//   MergeRoots 在首次注册时写入、后续保存时保留，这里直接返回即可。
	//   （历史脏数据：若个别根目录 added_at 曾因旧 bug 被重置为同一天，
	//     可重新导入或由用户手动调整；不再用文件创建时间掩盖。）

	return roots
}

// SaveRootsWithMeta 保存导入根目录（含元数据）
// ★ 修复：改为增量合并而非全量替换。前端传入的列表可能是不完整子集
//   （启动时 importedRoots 未加载完、或过滤/删除操作后），全量替换会误删
//   registeredRoots 与 DB 中已存在的注册根（真实库中已出现 4 个孤儿 root_path）。
//   删除只应通过 RemoveFolder / RemoveRoot 显式进行。
// existingNested 检查 rootKey（小写 + 正斜杠规范化）是否与 existing 集合中
// 某个根目录构成父子嵌套。返回命中的根路径与是否嵌套。
func existingNested(rootKey string, existing map[string]bool) (string, bool) {
	for e := range existing {
		eKey := strings.ToLower(strings.ReplaceAll(e, "\\", "/"))
		if eKey != rootKey &&
			(strings.HasPrefix(rootKey, eKey+"/") || strings.HasPrefix(eKey, rootKey+"/")) {
			return e, true
		}
	}
	return "", false
}

// removeNestedRoots 历史遗留的自愈逻辑：旧版本会移除位于其它已注册根目录之内的
// 嵌套根，以防同一批图片被两套根目录重复索引。乙方案起，父子嵌套已受支持（数据层
// 按完整路径去重，侧栏折叠展示），因此本函数不再删除嵌套根，仅保留为兼容调用。
func (a *App) removeNestedRoots() {
	// 乙方案：嵌套根允许存在，无需自愈移除。
	return
}

func (a *App) SaveRootsWithMeta(roots []database.ImportedRoot) map[string]interface{} {
	a.mu.Lock()
	// 保留现有注册根
	existing := make(map[string]bool, len(a.registeredRoots))
	for root := range a.registeredRoots {
		existing[root] = true
	}
	// ★ 诊断：SaveRootsWithMeta 入参 + 现有注册根
	{
		incoming := make([]string, 0, len(roots))
		for _, r := range roots {
			incoming = append(incoming, r.Path)
		}
		existingList := make([]string, 0, len(existing))
		for k := range existing {
			existingList = append(existingList, k)
		}
		fmt.Printf("[删除诊断] SaveRootsWithMeta: 传入 %d 个根=%v | 现有 registeredRoots=%v\n", len(roots), incoming, existingList)
	}
	// 传入根的元数据（按规范化路径索引），用于合并后保留
	metaMap := make(map[string]database.ImportedRoot, len(roots))
	for _, r := range roots {
		metaMap[strings.ToLower(strings.ReplaceAll(r.Path, "\\", "/"))] = r
	}
	hasNewRoot := false
	for _, r := range roots {
		rootKey := strings.ToLower(strings.ReplaceAll(r.Path, "\\", "/"))
		if !existing[r.Path] {
			// 大小写/分隔符不同但等价的路径视为已存在
			isNew := true
			for e := range existing {
				if strings.ToLower(strings.ReplaceAll(e, "\\", "/")) == rootKey {
					isNew = false
					break
				}
			}
			// ★ 乙方案：允许父子嵌套导入，不再跳过与现有根目录构成父子嵌套的路径。
			//   数据层按完整路径去重、侧栏折叠展示，嵌套根可与父根共存。
			if isNew {
				hasNewRoot = true
			}
		}
		existing[r.Path] = true
		a.registeredRoots[r.Path] = true
		if r.FolderType != "" {
			a.folderTypes[r.Path] = r.FolderType
		}
	}
	// 构建合并后的完整列表（保留传入的元数据；MergeRoots 会再保留 DB 中已有的 display_name/handle_name）
	merged := make([]database.ImportedRoot, 0, len(existing))
	for root := range existing {
		mr := database.ImportedRoot{
			Path:       root,
			Name:       filepath.Base(root),
			FolderType: a.folderTypes[root],
		}
		if m, ok := metaMap[strings.ToLower(strings.ReplaceAll(root, "\\", "/"))]; ok {
			if m.DisplayName != "" {
				mr.DisplayName = m.DisplayName
			}
			if m.HandleName != "" {
				mr.HandleName = m.HandleName
			}
			if m.AddedAt != "" {
				mr.AddedAt = m.AddedAt
			}
		}
		merged = append(merged, mr)
	}
	a.mu.Unlock()

	if a.userDataDB != nil {
		if err := a.userDataDB.MergeRoots(merged); err != nil {
			return map[string]interface{}{"success": false, "error": err.Error()}
		}
	}
	if hasNewRoot {
		go a.scanAllFolders()
	}
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
	// ★ 代理协议 http/https（空值兼容旧前端按 http 处理）
	ProxyProtocol string `json:"proxyProtocol,omitempty"`
	// ★ 请求超时秒数（来自前端 config.timeout，默认 120s）。
	//   原来硬编码 60s，与前端默认 120s 不一致，长任务会在 Go 层被提前掐断。
	Timeout int `json:"timeout,omitempty"`
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

		// ★ 超时与前端 config.timeout 保持一致：前端默认 120s，Go 层不再硬编码 60s。
		//   若前端未传（旧版）则用 120s 兜底，避免长任务被 Go 层提前掐断。
		timeoutSec := req.Timeout
		if timeoutSec <= 0 {
			timeoutSec = 120
		}
		client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
		if req.ProxyHost != "" && req.ProxyPort > 0 {
			// 支持 http/https 代理协议（socks 暂不支持，需 x/net/proxy）
			scheme := req.ProxyProtocol
			if scheme != "http" && scheme != "https" {
				scheme = "http"
			}
			proxyURL, _ := url.Parse(fmt.Sprintf("%s://%s:%d", scheme, req.ProxyHost, req.ProxyPort))
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
		// ★ 自愈历史遗留的嵌套根目录（同一子目录曾被重复导入），
		// 必须在补扫之前做，否则嵌套根会被当作"缺失"再次扫描。
		a.removeNestedRoots()

		a.ensureImageIndex()

		// ★ 启动速度优化：幽灵清理（全库 os.Stat，慢盘上数千次约 2-3 秒）延迟到
		//   首次 GetFolders 之后，避免与侧边栏初始加载抢磁盘 IO。
		go func() {
			time.Sleep(3 * time.Second)
			a.pruneDeadFolders()
		}()

		// ★ 启动速度优化：预热"子文件夹缩略图预览"缓存。用户开启了预览的根目录
		//   （如 K:/bid）在首次 GetFolders 时会逐个文件夹 SQLite 查询取预览，
		//   提前后台算好让首次 GetFolders 直接命中内存缓存。
		go func() {
			time.Sleep(2 * time.Second)
			if previewRoots := a.loadPreviewRoots(); len(previewRoots) > 0 {
				a.collectPreviewData(previewRoots)
			}
		}()

		// ★ 启动速度优化：两个重后台任务（全库 SHA256 / 元数据回填）延迟启动，
		//   不抢占启动期与首次浏览的磁盘/CPU；且各自限速（见各函数内部 pacing）。
		time.Sleep(20 * time.Second)
		// 后台回填 content_hash，用于图片去重
		go a.BackfillContentHashes()
		// ★ 后台回填搜索索引缺失的生成参数元数据（Model/LoRA/CFG 等），
		//   旧扫描写入的 params_json 为空导致按参数搜索不到；分批节流，不阻塞启动
		time.Sleep(10 * time.Second)
		go a.backfillImageMetadata()
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
	// ★ 计数持久化修复：先停掉后台自动预生成（避免生成到一半被截断），
	//   再把当前缩略图计数写盘。否则新导入文件夹的计数只在内存里涨，
	//   重启后 loadThumbCountsFromDisk 因 bbolt key 数变化而跳过缓存 → 计数短暂错误/为 0。
	//   若预生成被打断（生成未完成），则不持久化——保留旧文件，
	//   重启时 key 数不匹配 → 触发重算自愈，避免"部分计数被当成有效"。
	interrupted := a.stopAutoPreGen()
	if !interrupted {
		a.persistThumbCounts()
	}
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

func (a *App) openThumbDB() error {
	a.thumbDBMu.Lock()
	defer a.thumbDBMu.Unlock()
	return a.openThumbDBLocked()
}

func (a *App) openThumbDBLocked() error {
	if a.thumbDB != nil {
		return nil
	}

	dbPath := a.GetThumbDir()
	if dbPath == "" {
		return fmt.Errorf("缩略图数据库路径为空")
	}
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("无法创建缩略图目录: %w", err)
	}

	// 崩溃后文件锁可能短暂残留，给 5s 超时
	// ★ NoSync: 缩略图是可再生的缓存，崩溃丢失可重新生成，不值得每次事务 fsync。
	//   默认 bbolt 每次 Update 都 fsync，高并发写缩略图时磁盘同步成为吞吐瓶颈，
	//   这正是"调高并发数但速度提升不明显"的主要原因之一。
	// ★ 打点：openThumbDB 曾观测到 24~27s（平时 <0.2s），逐步骤计时定位瓶颈。
	dbStepT0 := time.Now()
	db, err := bbolt.Open(dbPath, 0644, &bbolt.Options{Timeout: 5 * time.Second, NoSync: true})
	if err != nil {
		logStartupf("ThumbDB: bbolt.Open 失败（%s）: %v", time.Since(dbStepT0).Round(time.Millisecond), err)
		return fmt.Errorf("无法打开 BoltDB: %w", err)
	}
	logStartupf("ThumbDB: bbolt.Open 完成（%s）", time.Since(dbStepT0).Round(time.Millisecond))
	dbStepT0 = time.Now()
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("thumbs"))
		return err
	}); err != nil {
		db.Close()
		logStartupf("ThumbDB: CreateBucketIfNotExists 失败（%s）: %v", time.Since(dbStepT0).Round(time.Millisecond), err)
		return fmt.Errorf("无法初始化 BoltDB bucket: %w", err)
	}
	logStartupf("ThumbDB: CreateBucketIfNotExists 完成（%s）", time.Since(dbStepT0).Round(time.Millisecond))
	a.thumbDB = db
	fmt.Printf("[缩略图] BoltDB 已打开: %s\n", dbPath)

	// ★ 防乌龙：打开的库为空，但数据目录下存在带数据的库 → 强警告。
	//   这种情况几乎都是 thumbDir 设置指向了错误目录（如漏了 \user），
	//   会导致"数量 0 + 全量重新生成缩略图"的困惑。
	// ★ 性能：bbolt 的 Bucket.Stats() 会遍历整个 bucket 的 B 树（读全部页）。
	//   本库 488K key / 17GB，冷缓存下实测 20.9s——不能每次启动都做全量遍历。
	//   因此仅当库文件很小（<1MB，只可能是空库或近乎空库）才统计 key 数；
	//   大文件必然有数据，直接跳过这条防乌龙检查（keyN 保持 -1 表示未统计）。
	dbStepT0 = time.Now()
	keyN := -1
	if info, err := os.Stat(dbPath); err == nil && info.Size() < 1<<20 {
		_ = db.View(func(tx *bbolt.Tx) error {
			if b := tx.Bucket([]byte("thumbs")); b != nil {
				keyN = b.Stats().KeyN
			}
			return nil
		})
	} else {
		logStartupf("ThumbDB: 大库跳过 KeyN 统计（%s）", time.Since(dbStepT0).Round(time.Millisecond))
	}
	logStartupf("ThumbDB: KeyN 统计完成 key=%d（%s）", keyN, time.Since(dbStepT0).Round(time.Millisecond))
	if keyN == 0 && a.userDataDir != "" {
		altPath := filepath.Join(a.userDataDir, "thumbnails.db")
		if altPath != dbPath {
			if altN := countThumbKeysInFile(altPath); altN > 0 {
				fmt.Printf("[缩略图] ⚠ 警告：当前打开的缩略图库为空（0 张），但数据目录下有 %d 张缩略图的库:\n", altN)
				fmt.Printf("[缩略图] ⚠   当前库: %s\n", dbPath)
				fmt.Printf("[缩略图] ⚠   数据目录库: %s\n", altPath)
				fmt.Printf("[缩略图] ⚠   请检查 thumbDir 设置是否指向了正确目录，避免误用空库重新生成全部缩略图\n")
			}
		}
	}
	return nil
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
