//go:build !bindings

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"local-gallery/internal/database"

	"github.com/davidbyttow/govips/v2/vips"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"go.etcd.io/bbolt"
)

const thumbMaxSize = 500
const thumbJPEGQuality = 85

// 可调缩略图信号量：动态控制并发生成数量
var (
	thumbGenLocks sync.Map // map[string]*sync.Mutex
	thumbSemMu    sync.RWMutex
	thumbSem      = make(chan struct{}, max(goruntime.NumCPU(), 2))
	thumbSemSize  = int32(max(goruntime.NumCPU(), 2))

	// onDemandSem 前台按需缩略图专用的并发限流，与后台预生成的 thumbSem 分开。
	// ★ 关键：图廊缩略图走 /thumb(serveThumbnail 内联生成)，而预览大图走 /image
	//   (enqueueHighWait，无限流)。若 on-demand 也去抢后台预生成的 thumbSem，
	//   一旦它被调到很小(如 2，为后台让路)，前台图廊缩略图就卡在低并发→空白，
	//   而预览依旧 HIGH 优先飞快——表现为"预览能出图、图廊不出图"。
	//   单独一个更宽松的预算(max(4,CPU)，上限≈12=浏览器连接池)让前台快速出图，
	//   后台预生成仍用 thumbSem 限流、互不干扰。onDemandActive 已使预生成让位，
	//   两者不会叠加超订阅主进程 vips。
	onDemandSem = make(chan struct{}, onDemandSemBudget())

	// ★ onDemandActive：是否有前台按需缩略图生成正在进行（正读原图）。
	//   自动预生成据此让出磁盘——用户点击/浏览文件夹时，on-demand 优先获得
	//   磁盘读原图，预生成暂停，避免大批量导入时前台点击被子文件夹缩略图排队拖慢。
	onDemandActive int32
)

var thumbBucket = []byte("thumbs")

// ★ 缩略图内存缓存：把热门缩略图字节放 RAM，避免反复触发内核从 12GB bbolt(mmap)
//   换页 —— Task Manager 里表现为 System 进程疯狂读盘（机械盘随机寻道）。
//   key = imageID|thumbGeneration，缩略图重新生成时版本号递增 → 缓存自动失效。
var (
	thumbMemCacheMu   sync.Mutex
	thumbMemCache     = map[string][]byte{}
	thumbMemCacheKeys []string // FIFO 淘汰顺序
)

const thumbMemCacheMax = 2000 // 约 200MB（500px JPEG 平均 ~100KB）

func thumbMemKey(imageID string) string {
	return fmt.Sprintf("%s|%d", imageID, thumbGeneration.Load())
}

func thumbMemGet(imageID string) []byte {
	thumbMemCacheMu.Lock()
	defer thumbMemCacheMu.Unlock()
	return thumbMemCache[thumbMemKey(imageID)]
}

func thumbMemSet(imageID string, b []byte) {
	if len(b) == 0 {
		return
	}
	key := thumbMemKey(imageID)
	thumbMemCacheMu.Lock()
	defer thumbMemCacheMu.Unlock()
	if _, ok := thumbMemCache[key]; !ok {
		thumbMemCacheKeys = append(thumbMemCacheKeys, key)
	}
	thumbMemCache[key] = b
	// FIFO 淘汰最旧，防止无限增长
	for len(thumbMemCacheKeys) > thumbMemCacheMax {
		old := thumbMemCacheKeys[0]
		thumbMemCacheKeys = thumbMemCacheKeys[1:]
		delete(thumbMemCache, old)
	}
}

func thumbMemRemove(imageID string) {
	thumbMemCacheMu.Lock()
	delete(thumbMemCache, thumbMemKey(imageID))
	// 惰性清理失效 key：只在达到上限时压缩一次，避免每次删除都遍历
	if len(thumbMemCache) < thumbMemCacheMax/2 {
		newKeys := thumbMemCacheKeys[:0]
		for _, k := range thumbMemCacheKeys {
			if _, ok := thumbMemCache[k]; ok {
				newKeys = append(newKeys, k)
			}
		}
		thumbMemCacheKeys = newKeys
	}
	thumbMemCacheMu.Unlock()
}

// thumbFailSentinel 占位标记：图片无法生成缩略图（损坏/不支持的格式/已删除）
var thumbFailSentinel = []byte{0}

// thumbSemAcquire 获取信号量，返回释放函数
func thumbSemAcquire() func() {
	thumbSemMu.RLock()
	sem := thumbSem
	thumbSemMu.RUnlock()
	sem <- struct{}{}
	return func() { <-sem }
}

// onDemandSemBudget 前台按需缩略图并发预算：至少 4，随 CPU 提升，封顶 12(≈浏览器连接池)。
//  前台浏览必须"快"，不能像后台预生成那样被压到很小并发。
func onDemandSemBudget() int {
	n := goruntime.NumCPU()
	if n < 4 {
		n = 4
	}
	if n > 12 {
		n = 12
	}
	return n
}

// onDemandSemAcquire 获取前台 on-demand 专用信号量，返回释放函数。
//  不参与后台预生成的 thumbSem，前台图廊缩略图因此不被后台并发预算卡住。
func onDemandSemAcquire() func() {
	onDemandSem <- struct{}{}
	return func() { <-onDemandSem }
}

// ==================== 全局设置（独立于用户数据目录） ====================

// getGlobalSettingsPath 返回全局设置文件路径，位于 exe 所在目录，与用户数据目录无关
func getGlobalSettingsPath() string {
	execDir, _ := os.Getwd()
	return filepath.Join(execDir, ".gallery-settings.json")
}

func readGlobalSettings() map[string]interface{} {
	data := make(map[string]interface{})
	bytes, err := os.ReadFile(getGlobalSettingsPath())
	if err != nil {
		return data
	}
	json.Unmarshal(bytes, &data)
	return data
}

func writeGlobalSettings(data map[string]interface{}) error {
	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON 序列化失败: %w", err)
	}
	return os.WriteFile(getGlobalSettingsPath(), bytes, 0644)
}

// SetThumbConcurrency 设置缩略图并发生成数（0 表示恢复默认 = CPU 核心数）
func (a *App) SetThumbConcurrency(n int) map[string]interface{} {
	if n <= 0 {
		n = max(goruntime.NumCPU(), 2)
	}
	if n < 1 {
		n = 1
	}
	if n > 64 {
		n = 64
	}

	thumbSemMu.Lock()
	old := thumbSem
	thumbSem = make(chan struct{}, n)
	thumbSemSize = int32(n)
	thumbSemMu.Unlock()

	// 排空旧 channel（不会有新请求使用它了）
	for len(old) > 0 {
		<-old
	}

	current := readGlobalSettings()
	current["thumbConcurrency"] = n
	writeGlobalSettings(current)

	// ★ 杀掉运行中的 worker：下次生成时按新并发重新 spawn，vips 线程池立即生效
	a.killThumbWorker()

	fmt.Printf("[缩略图] 并发数已更新为 %d\n", n)
	return map[string]interface{}{"success": true, "thumbConcurrency": n}
}

// loadThumbSettings 启动时从全局设置恢复缩略图配置
func (a *App) loadThumbSettings() {
	data := readGlobalSettings()

	// 恢复并发数
	if n, ok := data["thumbConcurrency"].(float64); ok && n >= 1 && n <= 64 {
		thumbSemMu.Lock()
		old := thumbSem
		thumbSem = make(chan struct{}, int(n))
		thumbSemSize = int32(n)
		for len(old) > 0 {
			<-old
		}
		thumbSemMu.Unlock()
		fmt.Printf("[缩略图] 已从设置恢复并发数: %d\n", int(n))
	}
}

// GetThumbConcurrency 返回当前缩略图并发数
func (a *App) GetThumbConcurrency() int {
	thumbSemMu.RLock()
	defer thumbSemMu.RUnlock()
	return int(thumbSemSize)
}

// getThumbKernel 从全局设置中读取缩略图缩放算法，默认 Lanczos3
func (a *App) getThumbKernel() vips.Kernel {
	data := readGlobalSettings()
	if k, ok := data["thumbKernel"].(string); ok {
		switch k {
		case "nearest":
			return vips.KernelNearest
		case "linear":
			return vips.KernelLinear
		case "cubic":
			return vips.KernelCubic
		case "mitchell":
			return vips.KernelMitchell
		case "lanczos2":
			return vips.KernelLanczos2
		case "lanczos3":
			return vips.KernelLanczos3
		}
	}
	return vips.KernelLanczos3
}

// SetThumbKernel 设置缩略图缩放算法
func (a *App) SetThumbKernel(kernel string) map[string]interface{} {
	valid := map[string]bool{
		"nearest": true, "linear": true, "cubic": true,
		"mitchell": true, "lanczos2": true, "lanczos3": true,
	}
	if !valid[kernel] {
		return map[string]interface{}{"success": false, "error": "无效的缩放算法: " + kernel}
	}

	current := readGlobalSettings()
	current["thumbKernel"] = kernel
	writeGlobalSettings(current)

	fmt.Printf("[缩略图] 缩放算法已更新为 %s\n", kernel)
	return map[string]interface{}{"success": true, "thumbKernel": kernel}
}

// GetThumbKernel 返回当前缩放算法
func (a *App) GetThumbKernel() string {
	k := a.getThumbKernel()
	switch k {
	case vips.KernelNearest:
		return "nearest"
	case vips.KernelLinear:
		return "linear"
	case vips.KernelCubic:
		return "cubic"
	case vips.KernelMitchell:
		return "mitchell"
	case vips.KernelLanczos2:
		return "lanczos2"
	default:
		return "lanczos3"
	}
}

// getThumbDBPath 返回 BoltDB 文件路径（优先使用全局设置中的 thumbDir）
func (a *App) getThumbDBPath() string {
	if a.userDataDir == "" {
		return ""
	}
	data := readGlobalSettings()
	if dir, ok := data["thumbDir"].(string); ok && dir != "" {
		if filepath.Ext(dir) == ".db" {
			return dir
		}
		return filepath.Join(dir, "thumbnails.db")
	}
	return filepath.Join(a.userDataDir, "thumbnails.db")
}

// GetThumbDir 返回缩略图 BoltDB 文件路径
func (a *App) GetThumbDir() string {
	return a.getThumbDBPath()
}

// getThumbDir 内部使用
func (a *App) getThumbDir() string {
	return a.getThumbDBPath()
}

// ensureThumbDB 打开缩略图数据库；用于热切换失败或启动锁短暂残留后的懒恢复
func (a *App) ensureThumbDB() error {
	a.thumbDBMu.Lock()
	defer a.thumbDBMu.Unlock()
	return a.openThumbDBLocked()
}

// generateThumbnail 用 libvips thumbnail API 生成 JPEG 缩略图（shrink-on-load），写入 BoltDB
func (a *App) generateThumbnail(srcPath string, imageID string) error {
	if err := a.ensureThumbDB(); err != nil {
		return err
	}

	// 跳过空路径
	if srcPath == "" {
		return fmt.Errorf("图片路径为空: %s", imageID)
	}

	// 跳过 0 字节文件，防止 vips 崩溃
	if info, err := os.Stat(srcPath); err != nil {
		// ★ 自愈：源文件已被删除 → 清除该图片的所有残留（内存/DB/缩略图），
		//   图廊不再残留破图（文件夹仍在但文件被删的"文件级幽灵"）。
		a.purgeMissingImage(imageID)
		a.markThumbFailed(imageID)
		return fmt.Errorf("文件不存在或无法访问: %s", srcPath)
	} else if info.Size() == 0 {
		a.markThumbFailed(imageID)
		return fmt.Errorf("文件为空: %s", srcPath)
	}

	// 视频文件：存储黑色占位 JPEG（无需帧提取）
	if isVideoFile(filepath.Base(srcPath)) {
		placeholder := createVideoPlaceholderJPEG()
		return a.thumbDB.Batch(func(tx *bbolt.Tx) error {
			b := tx.Bucket(thumbBucket)
			return b.Put([]byte(imageID), placeholder)
		})
	}

	// ★ 安全预检：读文件头魔数判断是否为有效图片格式
	//    Go 标准库不支持 SVG/WEBP/AVIF 等，所以不能用 image.DecodeConfig
	//    用魔数白名单：只拒绝明显不是图片的二进制文件
	if !isImageByMagic(srcPath) {
		a.markThumbFailed(imageID)
		return fmt.Errorf("文件头魔数不匹配任何已知图片格式: %s", srcPath)
	}

	// ★ 生成：优先经独立 worker 进程（可被杀 = 可立即停读原图）。
	//   worker 失败时若文件夹已不再被关注（切换走了）则直接放弃、不回退；
	//   仅当文件夹仍被关注才回退进程内 libvips（保证生成不中断）。
	jpegBytes, err := a.generateViaWorker(srcPath)
	if err != nil {
		if fk := a.imageFolderKey(srcPath); fk != "" && isFolderAbandoned(fk) {
			return fmt.Errorf("文件夹已切换，取消缩略图生成: %s", imageID)
		}
		// ★ 内联回退必须走 thumbSem 限流：worker 死时预生成全部落到主进程内联，
		//   若不限流会和前台 on-demand 一起超订阅主进程 vips 线程池/磁盘 → 每张都慢、
		//   图廊靠后卡片在浏览器超时窗口内等不到 → Load failed（表现为"没出图要刷新"）。
		//   限流后总并发=CPU 核数，且 pre-gen 会在 on-demand 浏览时让出槽位（浏览优先）。
		release := thumbSemAcquire()
		jpegBytes, err = generateThumbnailBytes(srcPath, thumbMaxSize, thumbJPEGQuality)
		release()
		if err != nil {
			a.markThumbFailed(imageID)
			return fmt.Errorf("缩略图生成失败: %w", err)
		}
	}

	// 写入 BoltDB（★ Batch：高并发时合并写事务 + 只 fsync 一次，缓解写锁瓶颈）
	err = a.thumbDB.Batch(func(tx *bbolt.Tx) error {
		b := tx.Bucket(thumbBucket)
		return b.Put([]byte(imageID), jpegBytes)
	})
	if err != nil {
		a.markThumbFailed(imageID)
		return fmt.Errorf("写入 BoltDB 失败: %w", err)
	}

	a.incrementThumbCount(imageID)
	return nil
}

// generateThumbnailDirect 进程内直连生成（不经 worker）：供后台自动预生成使用，
// 不会被文件夹切换的 kill 打断，保证预生成能跑完、浏览时缩略图已就位。
func (a *App) generateThumbnailDirect(srcPath string, imageID string) error {
	if err := a.ensureThumbDB(); err != nil {
		return err
	}
	if srcPath == "" {
		return fmt.Errorf("图片路径为空: %s", imageID)
	}
	if info, err := os.Stat(srcPath); err != nil {
		a.purgeMissingImage(imageID)
		a.markThumbFailed(imageID)
		return fmt.Errorf("文件不存在或无法访问: %s", srcPath)
	} else if info.Size() == 0 {
		a.markThumbFailed(imageID)
		return fmt.Errorf("文件为空: %s", srcPath)
	}
	if isVideoFile(filepath.Base(srcPath)) {
		placeholder := createVideoPlaceholderJPEG()
		return a.thumbDB.Batch(func(tx *bbolt.Tx) error {
			b := tx.Bucket(thumbBucket)
			return b.Put([]byte(imageID), placeholder)
		})
	}
	if !isImageByMagic(srcPath) {
		a.markThumbFailed(imageID)
		return fmt.Errorf("文件头魔数不匹配任何已知图片格式: %s", srcPath)
	}
	jpegBytes, err := generateThumbnailBytes(srcPath, thumbMaxSize, thumbJPEGQuality)
	if err != nil {
		a.markThumbFailed(imageID)
		return fmt.Errorf("缩略图生成失败: %w", err)
	}
	err = a.thumbDB.Batch(func(tx *bbolt.Tx) error {
		b := tx.Bucket(thumbBucket)
		return b.Put([]byte(imageID), jpegBytes)
	})
	if err != nil {
		a.markThumbFailed(imageID)
		return fmt.Errorf("写入 BoltDB 失败: %w", err)
	}
	a.incrementThumbCount(imageID)
	return nil
}

// generateThumbnailBytes 纯 libvips 生成（worker 进程与主进程 inline 回退共用）。
func generateThumbnailBytes(srcPath string, maxSize, quality int) ([]byte, error) {
	img, err := vips.NewThumbnailFromFile(srcPath, maxSize, maxSize, vips.InterestingNone)
	if err != nil {
		return nil, fmt.Errorf("vips 缩略图生成失败: %w", err)
	}
	defer img.Close()

	ep := vips.NewJpegExportParams()
	ep.Quality = quality
	ep.StripMetadata = true

	jpegBytes, _, err := img.ExportJpeg(ep)
	if err != nil {
		return nil, fmt.Errorf("vips 导出 JPEG 失败: %w", err)
	}
	return jpegBytes, nil
}

// isImageByMagic 读文件头魔数判断是否为已知图片格式
// 策略：只拦截"扩展名是图片但魔数是完全无关格式"的损坏文件（如 TGA 命名为 .jpg）
// 对于魔数不明确但扩展名合理的文件，信任扩展名放行
func isImageByMagic(filePath string) bool {
	f, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer f.Close()

	var header [12]byte
	n, _ := f.Read(header[:])
	if n < 4 {
		return false
	}

	// 已知图片魔数 → 放行
	if header[0] == 0xFF && header[1] == 0xD8 && header[2] == 0xFF {
		return true
	} // JPEG
	if header[0] == 0x89 && header[1] == 0x50 && header[2] == 0x4E && header[3] == 0x47 {
		return true
	} // PNG
	if header[0] == 0x47 && header[1] == 0x49 && header[2] == 0x46 && header[3] == 0x38 {
		return true
	} // GIF
	if header[0] == 0x42 && header[1] == 0x4D && n >= 6 { // BMP
		// BMP 文件头 14B: 2B魔数(BM) + 4B文件大小 + 4B保留(必须为0) + 4B偏移
		// 保留字段必须为 0，防止非 BMP 文件误入导致 libvips C 层崩溃
		reserved := uint32(header[6]) | uint32(header[7])<<8 | uint32(header[8])<<16 | uint32(header[9])<<24
		return reserved == 0
	}
	if header[0] == 0x52 && header[1] == 0x49 && header[2] == 0x46 && header[3] == 0x46 &&
		n >= 12 && header[8] == 0x57 && header[9] == 0x45 && header[10] == 0x42 && header[11] == 0x50 {
		return true
	} // WebP
	if header[0] == 0x49 && header[1] == 0x49 && header[2] == 0x2A && header[3] == 0x00 {
		return true
	} // TIFF LE
	if header[0] == 0x4D && header[1] == 0x4D && header[2] == 0x00 && header[3] == 0x2A {
		return true
	} // TIFF BE
	if header[0] == 0x00 && header[1] == 0x00 && header[2] == 0x01 && header[3] == 0x00 {
		return true
	} // ICO
	if header[0] == '<' {
		return true
	} // SVG/XML
	if n >= 8 && header[4] == 0x66 && header[5] == 0x74 && header[6] == 0x79 && header[7] == 0x70 {
		return true
	} // HEIC/AVIF

	// 已知非图片魔数 → 拦截（伪装扩展名的损坏文件）
	// TGA: 文件尾有 TRUEVISION 或开头并非 JPEG
	// RIFF 非 WebP: AVI/WAV 等
	if header[0] == 0x52 && header[1] == 0x49 && header[2] == 0x46 && header[3] == 0x46 {
		return false
	} // RIFF 但非 WebP
	// EXE/DLL: MZ
	if header[0] == 0x4D && header[1] == 0x5A {
		return false
	}
	// ZIP/DOCX/XLSX: PK
	if header[0] == 0x50 && header[1] == 0x4B {
		return false
	}
	// PDF
	if header[0] == 0x25 && header[1] == 0x50 && header[2] == 0x44 && header[3] == 0x46 {
		return false
	}
	// RAR
	if header[0] == 0x52 && header[1] == 0x61 && header[2] == 0x72 && header[3] == 0x21 {
		return false
	}
	// 7z
	if header[0] == 0x37 && header[1] == 0x7A && header[2] == 0xBC && header[3] == 0xAF {
		return false
	}
	// GZIP
	if header[0] == 0x1F && header[1] == 0x8B {
		return false
	}

	// 无法确认 → 信任扩展名放行（相机 RAW/CR2/NEF/ORF 等无法通过魔数确认）
	return true
}

// serveThumbnail 从 BoltDB 读取缩略图 JPEG 字节；如不存在则生成
func (a *App) serveThumbnail(imageID string) ([]byte, error) {
	debugSwitchf("[thumb] serveThumbnail 进入: id=%s", imageID)
	imagePath := a.resolveImagePath(imageID)
	if imagePath == "" {
		a.markThumbFailed(imageID)
		return nil, fmt.Errorf("图片未找到: %s", imageID)
	}

	if err := a.ensureThumbDB(); err != nil {
		return nil, err
	}

	// ★ 内存缓存优先：命中直接返回，不再触发内核从 12GB bbolt(mmap) 换页
	//   （削减 Task Manager 里 System 进程的疯狂读盘）
	if cached := thumbMemGet(imageID); cached != nil {
		debugSwitchf("[thumb] 内存缓存命中: id=%s", imageID)
		return cached, nil
	}

	// 先从 BoltDB 读取
	if a.thumbDB != nil {
		var jpegBytes []byte
		err := a.thumbDB.View(func(tx *bbolt.Tx) error {
			b := tx.Bucket(thumbBucket)
			v := b.Get([]byte(imageID))
			if v != nil {
				jpegBytes = make([]byte, len(v))
				copy(jpegBytes, v)
			}
			return nil
		})
		if err == nil && len(jpegBytes) > 0 {
			// 检查是否是失败标记
			if len(jpegBytes) == 1 && jpegBytes[0] == 0 {
				// 失败标记：检查原文件现在是否存在，如果存在则清除标记重新生成
				if imagePath != "" {
					if _, err := os.Stat(imagePath); err == nil {
						// 原文件存在，清除失败标记并重新生成
						a.thumbDB.Update(func(tx *bbolt.Tx) error {
							b := tx.Bucket(thumbBucket)
							return b.Delete([]byte(imageID))
						})
						// 继续往下走，重新生成
					} else {
						// 原文件不存在，返回失败
						return nil, fmt.Errorf("缩略图不可用且原文件不存在: %s", imageID)
					}
				}
				} else {
					thumbMemSet(imageID, jpegBytes)
					return jpegBytes, nil
				}
		}
	}

	// per-image 锁：同一张图不重复生成
	muI, _ := thumbGenLocks.LoadOrStore(imageID, &sync.Mutex{})
	mu := muI.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	// 双重检查
	if a.thumbDB != nil {
		var jpegBytes []byte
		a.thumbDB.View(func(tx *bbolt.Tx) error {
			b := tx.Bucket(thumbBucket)
			v := b.Get([]byte(imageID))
			if v != nil {
				jpegBytes = make([]byte, len(v))
				copy(jpegBytes, v)
			}
			return nil
		})
		if len(jpegBytes) > 0 {
			if len(jpegBytes) == 1 && jpegBytes[0] == 0 {
				return nil, fmt.Errorf("缩略图不可用: %s", imageID)
			}
			debugSwitchf("[thumb] bbolt 双重检查命中: id=%s", imageID)
			return jpegBytes, nil
		}
	}

	// ★ 文件夹切换取消：若该图片所属文件夹刚被"遗弃"（用户已切走），
	//   跳过 on-demand 生成——排队中的请求在等信号量之前就放弃，
	//   不再为已不看的内容读原图文件。已缓存缩略图不受影响（上面已返回）。
	if fk := a.imageFolderKey(imagePath); fk != "" && isFolderAbandoned(fk) {
		return nil, fmt.Errorf("文件夹已切换，取消缩略图生成: %s", imageID)
	}

	debugSwitchf("[thumb] 未命中, 排队生成: id=%s", imageID)

	// ★ 低优先级队列：大图读取（HIGH）永远先于缩略图生成（LOW）被调度，
	//   查看大图不会被排队的缩略图任务拖住。并发仍由 thumbSem 控制
	//   （任务内部 acquire），并发设置语义不变。
	var jpegBytes []byte
	var genErr error
	// ★ 浏览优先(before-semaphore)：在排队/抢 thumbSem 槽位之前就置 on-demand 标志。
	//   自动预生成 worker 循环的任务开头检查 onDemandActive(见 auto pre-gen 循环)，
	//   看到即让出磁盘与信号量。若等到拿到 thumbSem 槽位才置 1，on-demand 会在信号量
	//   上干等——而点击文件夹时 AbandonFolder 杀掉了 worker，预生成的 generateThumbnail
	//   退化为内联回退、正占着 thumbSem 槽位，于是前台点击迟迟不 thumbnailing、
	//   等预生成腾位后一次性爆发。前置标志让预生成立即让路，当前文件夹优先生成。
	atomic.AddInt32(&onDemandActive, 1)
	enqueueLowWait(func() {
		// ★ 无论正常完成还是中途 return(如文件夹已切走)都释放 on-demand 标志。
		defer atomic.AddInt32(&onDemandActive, -1)

		// ★ 前台 on-demand 用独立信号量(而非后台预生成的 thumbSem)：图廊缩略图
		//   必须快速生成，否则预览(/image,HIGH)飞快而图廊(/thumb)空白——
		//   "预览能出图、图廊不出图"就是这个机制。预算见 onDemandSemBudget()。
		tSem0 := time.Now()
		release := onDemandSemAcquire()
		debugSwitchf("[thumb] 拿到 on-demand 信号量: id=%s 等待=%.1fms", imageID, time.Since(tSem0).Seconds()*1000)
		defer release()

		// ★ 双重检查：排队到执行前用户可能已切走 → 仍放弃，不读原图。
		if fk := a.imageFolderKey(imagePath); fk != "" && isFolderAbandoned(fk) {
			genErr = fmt.Errorf("文件夹已切换，取消缩略图生成: %s", imageID)
			return
		}

		// ★ 前台按需生成走 generateThumbnail(优先 worker 进程、失败才内联回退)：
		//   worker 化后主进程不再被每张 130-183ms 的内联 libvips 生成占满——
		//   "点击文件夹慢"的根因就是主进程忙于给视口几十张大图现场生成缩略图，
		//   导致同一主进程里的 GetImages/UI 全被拖慢。生成挪到 worker 后主进程
		//   只发请求、等字节，点击/查询恢复流畅。worker 不可用(刚被杀/初始化中)
		//   时退回主进程内联，保证前台不失败。
		//   (on-demand 标志已在上方入队前置位，让自动预生成循环让出 worker/磁盘。)
		tGen0 := time.Now()
		err := a.generateThumbnail(imagePath, imageID)
		debugSwitchf("[thumb] 生成完成: id=%s err=%v 耗时=%.1fms", imageID, err, time.Since(tGen0).Seconds()*1000)
		if err != nil {
			fmt.Printf("[缩略图] 生成失败 %s: %v\n", imageID, err)
			a.markThumbFailed(imageID)
			genErr = err
			return
		}

		// 读取刚写入的数据
		a.thumbDB.View(func(tx *bbolt.Tx) error {
			b := tx.Bucket(thumbBucket)
			v := b.Get([]byte(imageID))
			if v != nil {
				jpegBytes = make([]byte, len(v))
				copy(jpegBytes, v)
			}
			return nil
		})
		thumbMemSet(imageID, jpegBytes)
	})
	if genErr != nil {
		return nil, genErr
	}
	return jpegBytes, nil
}

// markThumbFailed 标记图片无法生成缩略图（存占位符，计入进度，不再重试）
// 无论之前是否有缓存，都写入失败标记（因为原文件可能已损坏）
func (a *App) markThumbFailed(imageID string) {
	if a.thumbDB == nil {
		return
	}
	a.thumbDB.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(thumbBucket)
		return b.Put([]byte(imageID), thumbFailSentinel)
	})
}

// thumbGeneration 缩略图版本号，清缓存后递增，前端用于 URL 缓存破坏
var thumbGeneration atomic.Int64

// GetThumbGeneration 返回当前缩略图版本号
func (a *App) GetThumbGeneration() int64 {
	return thumbGeneration.Load()
}

// preGen 控制变量
var (
	preGenCancel    chan struct{}
	preGenCloseOnce sync.Once
	preGenPaused    int32 // atomic: 0=运行中, 1=已暂停
	preGenCond      = sync.NewCond(&sync.Mutex{})
	preGenMu        sync.Mutex
	preGenStatus    PreGenStatus
	preGenStatusMu  sync.RWMutex
)

// thumbCounts 缓存，避免每次 GetFolders 都扫描 BoltDB + images
var (
	cachedThumbCounts map[string]int
	thumbCountsValid  bool
	thumbCountsMu     sync.RWMutex
	thumbNotifyTimer  *time.Timer
	thumbNotifyMu     sync.Mutex
	thumbNotifyDirty  bool // 冷却期内有新的增量，冷却结束后需要补发
	thumbFolderCounts map[string]int // folderKey → 最新缓存缩略图计数（增量通知缓冲，前端定向更新徽章）
)

const thumbNotifyThrottle = 300 * time.Millisecond

// StartPreGenThumbs 开始后台预生成缩略图（按多个文件夹）
func (a *App) StartPreGenThumbs(folders []string) map[string]interface{} {
	// 规范化路径分隔符，与 folderIndex / folderCount 的 key 保持一致
	normFolders := make([]string, len(folders))
	for i, f := range folders {
		normFolders[i] = strings.ReplaceAll(f, "\\", "/")
	}

	// 收集所有选中文件夹及其子文件夹下的 folderKey
	a.mu.RLock()
	var keysToLoad []string
	for _, folder := range normFolders {
		prefix := folder + "/"
		for k := range a.folderIndex {
			if k == folder || strings.HasPrefix(k, prefix) {
				keysToLoad = append(keysToLoad, k)
			}
		}
	}
	a.mu.RUnlock()

	// ★ 直接从 SQL 拉取条目，避免 LRU 淘汰造成漏数据
	seen := make(map[string]bool)
	var entries []*ImageEntry
	for _, k := range keysToLoad {
		root, folderRel := a.splitFolderKey(k)
		dbEntries, err := a.imageDB.LoadImageCacheByFolder(root, folderRel)
		if err != nil {
			continue
		}
		for _, e := range dbEntries {
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			entries = append(entries, toImageEntry(e))
		}
	}

	if len(entries) == 0 {
		return map[string]interface{}{"success": false, "message": "文件夹无图片数据"}
	}

	// 停止旧的预生成任务
	preGenMu.Lock()
	preGenCloseOnce.Do(func() {
		if preGenCancel != nil {
			close(preGenCancel)
		}
	})
	preGenCancel = make(chan struct{})
	preGenCloseOnce = sync.Once{}
	atomic.StoreInt32(&preGenPaused, 0)
	preGenCond.Broadcast()
	preGenMu.Unlock()

	folderLabel := strings.Join(normFolders, ", ")
	preGenStatusMu.Lock()
	preGenStatus = PreGenStatus{
		Running: true,
		Folder:  folderLabel,
		Total:   len(entries),
	}
	preGenStatusMu.Unlock()

	go a.runPreGen(folderLabel, entries)
	fmt.Printf("[预生成] 开始为 %s 生成缩略图，共 %d 张\n", folderLabel, len(entries))
	return map[string]interface{}{"success": true, "total": len(entries)}
}

// runPreGen 并发生成缩略图（按设置的并发数），跳过已有缓存，支持暂停/恢复/取消
func (a *App) runPreGen(folder string, entries []*ImageEntry) {
	defer func() {
		preGenStatusMu.Lock()
		preGenStatus.Running = false
		preGenStatus.Paused = false
		preGenStatusMu.Unlock()
		fmt.Printf("[预生成] %s: 完成 (生成 %d, 跳过 %d, 失败 %d)\n",
			folder, preGenStatus.Done, preGenStatus.Skipped, preGenStatus.Failed)
		a.emitThumbProgress()
	}()

	// 每秒输出速率
	stopRate := make(chan struct{})
	defer close(stopRate)
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		var lastDone, lastSkipped int
		tickN := 0
		for {
			select {
			case <-ticker.C:
				tickN++
				preGenStatusMu.RLock()
				done := preGenStatus.Done
				skipped := preGenStatus.Skipped
				total := preGenStatus.Total
				paused := preGenStatus.Paused
				preGenStatusMu.RUnlock()
				delta := (done + skipped) - (lastDone + lastSkipped)
				rate := float64(delta) / 3.0
				pauseTag := ""
				if paused {
					pauseTag = " [暂停中]"
				}
				fmt.Printf("[预生成] 进度 %d/%d (%.1f/s)%s\n", done+skipped, total, rate, pauseTag)
				lastDone = done
				lastSkipped = skipped
				// 每 ~12 秒通知前端刷新缩略图进度
				if tickN%4 == 0 {
					a.emitThumbProgress()
				}
			case <-stopRate:
				return
			}
		}
	}()

	// 确定并发数
	thumbSemMu.RLock()
	concurrency := int(thumbSemSize)
	thumbSemMu.RUnlock()
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(entries) {
		concurrency = len(entries)
	}

	tasks := make(chan *ImageEntry, concurrency*2)
	var wg sync.WaitGroup

	// 启动 worker goroutines
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entry := range tasks {
				// 检查暂停
				preGenCond.L.Lock()
				for atomic.LoadInt32(&preGenPaused) == 1 {
					preGenStatusMu.Lock()
					preGenStatus.Paused = true
					preGenStatusMu.Unlock()
					preGenCond.Wait()
				}
				preGenStatusMu.Lock()
				preGenStatus.Paused = false
				preGenStatusMu.Unlock()
				preGenCond.L.Unlock()

				// 检查取消
				select {
				case <-preGenCancel:
					return
				default:
				}

				// ★ 浏览优先：on-demand 正在生成时，runPreGen 也让出磁盘/信号量，
				//   与 auto pre-gen 循环(检查 onDemandActive)保持一致。否则若 worker
				//   已死(点击切换被 AbandonFolder 杀掉)，generateThumbnail 在此退化为
				//   内联回退、占着 thumbSem，前台点击的缩略图会被本预生成挡住干等。
				for atomic.LoadInt32(&onDemandActive) > 0 {
					select {
					case <-preGenCancel:
						return
					case <-time.After(200 * time.Millisecond):
					}
				}

				// 跳过已有缓存
				if a.thumbDB != nil {
					hasCache := false
					a.thumbDB.View(func(tx *bbolt.Tx) error {
						if b := tx.Bucket(thumbBucket); b != nil && b.Get([]byte(entry.ID)) != nil {
							hasCache = true
						}
						return nil
					})
					if hasCache {
						preGenStatusMu.Lock()
						preGenStatus.Skipped++
						preGenStatusMu.Unlock()
						continue
					}
				}

				// per-image 锁
				muI, _ := thumbGenLocks.LoadOrStore(entry.ID, &sync.Mutex{})
				mu := muI.(*sync.Mutex)
				mu.Lock()

				// 双重检查
				skipped := false
				if a.thumbDB != nil {
					a.thumbDB.View(func(tx *bbolt.Tx) error {
						if b := tx.Bucket(thumbBucket); b != nil && b.Get([]byte(entry.ID)) != nil {
							skipped = true
						}
						return nil
					})
				}
				if skipped {
					mu.Unlock()
					preGenStatusMu.Lock()
					preGenStatus.Skipped++
					preGenStatusMu.Unlock()
					continue
				}

				if err := a.generateThumbnail(entry.Path, entry.ID); err != nil {
					preGenStatusMu.Lock()
					preGenStatus.Failed++
					preGenStatusMu.Unlock()
				} else {
					preGenStatusMu.Lock()
					preGenStatus.Done++
					preGenStatusMu.Unlock()
				}
				mu.Unlock()
			}
		}()
	}

	// 投放任务
	for _, entry := range entries {
		select {
		case tasks <- entry:
		case <-preGenCancel:
			close(tasks)
			wg.Wait()
			return
		}
	}
	close(tasks)
	wg.Wait()
}

// ==================== 自动预生成（导入/扫描后后台低优补齐） ====================

var (
	autoPreGenCancel   chan struct{}
	autoPreGenCancelMu sync.Mutex
	autoPreGenRunning  int32 // 1=后台自动预生成进行中（关闭时用于判断是否被打断）
)

// stopAutoPreGen 停止后台自动预生成（关闭流程调用）。返回是否"被打断"（运行中强停）。
// 幂等：重复调用安全。
func (a *App) stopAutoPreGen() bool {
	autoPreGenCancelMu.Lock()
	defer autoPreGenCancelMu.Unlock()
	running := atomic.LoadInt32(&autoPreGenRunning) == 1
	if autoPreGenCancel != nil {
		close(autoPreGenCancel)
		autoPreGenCancel = nil
	}
	return running
}

// triggerAutoPreGen 在扫描/导入新增图片后调用：低优先级后台补齐缺失缩略图。
// 与手动预生成（StartPreGenThumbs）互不干扰：独立取消通道、固定低并发、
// 不写 preGenStatus（设置页进度条不会被自动任务污染）。
// 生成是幂等的：bbolt 双重检查 + per-image 锁，与 on-demand / 手动预生成并发安全。
func (a *App) triggerAutoPreGen(label string, entries []*ImageEntry) {
	if len(entries) == 0 || a.thumbDB == nil {
		return
	}
	// 只保留缺失缩略图的条目，避免无谓调度
	todo := make([]*ImageEntry, 0, len(entries))
	for _, e := range entries {
		has := false
		a.thumbDB.View(func(tx *bbolt.Tx) error {
			if b := tx.Bucket(thumbBucket); b != nil && b.Get([]byte(e.ID)) != nil {
				has = true
			}
			return nil
		})
		if !has {
			todo = append(todo, e)
		}
	}
	if len(todo) == 0 {
		return
	}
	fmt.Printf("[自动预生成] 扫描新增 %d 张图片，后台低优生成缺失缩略图（%s）\n", len(todo), label)

	// 新的扫描会取消旧的自动任务（避免堆积）
	autoPreGenCancelMu.Lock()
	if autoPreGenCancel != nil {
		close(autoPreGenCancel)
	}
	autoPreGenCancel = make(chan struct{})
	cancel := autoPreGenCancel
	autoPreGenCancelMu.Unlock()

	// ★ 标记进行中：关闭时若被打断，则不持久化计数（留旧文件 → 重启重算自愈）
	atomic.StoreInt32(&autoPreGenRunning, 1)
	defer atomic.StoreInt32(&autoPreGenRunning, 0)

	// ★ 固定低并发（2）：不抢占交互式 on-demand 缩略图生成的 vips 额度，
	//   导入后再怎么扫都不会拖慢前台浏览。
	const workers = 2
	jobs := make(chan *ImageEntry, workers*2)
	var wg sync.WaitGroup
	// ★★ 本轮已被用户"切走"的文件夹集合：一旦跳过则整轮不再读它（不受 5s 过期影响）
	skippedFolders := make(map[string]bool)
	var skippedMu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				select {
				case <-cancel:
					return
				default:
				}
				// ★★ 切换中断：用户切走了该图片所属文件夹 → 本轮跳过，
				//   不再读目标文件夹原图做缩略图（制作缩略图是读原图，必须随切换停）。
				//   一旦跳过则整轮记住，避免过期后恢复读取。
				fk := a.imageFolderKey(e.Path)
				if fk != "" {
					skippedMu.Lock()
					skip := skippedFolders[fk]
					skippedMu.Unlock()
					if !skip && isFolderAbandoned(fk) {
						skippedMu.Lock()
						skippedFolders[fk] = true
						skippedMu.Unlock()
						skip = true
					}
					if skip {
						continue
					}
				}
				// ★ 浏览优先：用户正在点击/浏览文件夹时，on-demand 正在读原图做缩略图，
				//   自动预生成让出磁盘（否则大批量导入时磁盘被占满，前台点击排队）。
				//   等 on-demand 读原图告一段落再继续，且随时响应取消。
				for atomic.LoadInt32(&onDemandActive) > 0 {
					select {
					case <-cancel:
						return
					case <-time.After(200 * time.Millisecond):
					}
				}
				muI, _ := thumbGenLocks.LoadOrStore(e.ID, &sync.Mutex{})
				mu := muI.(*sync.Mutex)
				mu.Lock()
				// 双重检查：可能已被 on-demand 生成
				has := false
				a.thumbDB.View(func(tx *bbolt.Tx) error {
					if b := tx.Bucket(thumbBucket); b != nil && b.Get([]byte(e.ID)) != nil {
						has = true
					}
					return nil
				})
				if has {
					mu.Unlock()
					continue
				}
				// ★ 走 worker 子进程生成（generateThumbnail）——切换文件夹杀 worker 时，
				//   正在读原图的 vips 操作随进程被 OS 立即终止，读盘中断。
				if err := a.generateThumbnail(e.Path, e.ID); err != nil {
					fmt.Printf("[自动预生成] 失败 %s: %v\n", e.ID, err)
				}
				mu.Unlock()
			}
		}()
	}
	for _, e := range todo {
		select {
		case jobs <- e:
		case <-cancel:
			close(jobs)
			wg.Wait()
			return
		}
	}
	close(jobs)
	wg.Wait()
	fmt.Printf("[自动预生成] %s 完成（本轮生成 %d 张）\n", label, len(todo))
}

// ==================== 文件夹切换取消（遗弃集合） ====================
// 前端切换文件夹时调用 AbandonFolder 把离开的文件夹记为"已遗弃"，
// on-demand 缩略图生成（serveThumbnail）在真正读原图前检查该集合，
// 排队中/未开始的生成直接放弃，避免为已不看的内容读盘。
// 集合条目带 5 秒过期 + 重新聚焦时移除，不影响之后再次浏览该文件夹。

var (
	abandonedFoldersMu sync.Mutex
	abandonedFolders   = map[string]int64{} // folderKey → 被遗弃时间戳(毫秒)
)

// ★ 遗弃永久有效：切换文件夹后，旧文件夹的缩略图生成彻底停止，
//   直到用户重新聚焦（FocusFolder）该文件夹才恢复。
//   之前用 5 秒过期，导致切换后 5 秒生成又恢复读原图——用户看到的"没停"就是它。

// AbandonFolder 前端在离开某文件夹（切换视图）时调用：记录为"已遗弃"。
// ★ 不再杀 worker 进程：此前"每次切换都 killThumbWorker"会在用户快速切文件夹时
//   反复杀掉/重生 worker（重生含 vips 初始化停滞），且前台 on-demand 现走 worker
//   生成——杀 worker 等于把当前文件夹的缩略图生成打断。遗弃保护由调用方的
//   isFolderAbandoned 判断承担（on-demand / 自动预生成在发任务前都会检查并跳过，
//   worker 只是"生成字节"的无状态进程，不知道也无需知道文件夹归属）。
//   在途的原图读取最多完成当前 1 个任务即空闲，不会持续读被遗弃文件夹。
func (a *App) AbandonFolder(folderPath string) {
	if folderPath == "" {
		return
	}
	key := strings.ReplaceAll(folderPath, "\\", "/")
	abandonedFoldersMu.Lock()
	abandonedFolders[key] = time.Now().UnixMilli()
	abandonedFoldersMu.Unlock()
}

// FocusFolder 前端进入某文件夹时调用：从遗弃集合移除该文件夹及其所有祖先，
// 恢复该文件夹（及子孙）的生成。
// ★ 必须同时清除祖先：若根/父文件夹此前被遗弃，前缀匹配会把其下所有子文件夹
//   的生成全部拦截（浏览根目录后切走 → 整个树"Load failed"）。聚焦一个子文件夹
//   时它的祖先自然也在用户视野内，不应再被遗弃拦截。
// ★ 同时清除子孙：若此前离开过某子文件夹（它被遗弃），现在点进它的父/根目录，
//   该子文件夹也在视野内 → 一并解除遗弃，否则父目录视图里它的缩略图全部被取消
//   （表现为"部分不出图 / Load failed"）。
func (a *App) FocusFolder(folderPath string) {
	if folderPath == "" {
		return
	}
	key := strings.ReplaceAll(folderPath, "\\", "/")
	abandonedFoldersMu.Lock()
	defer abandonedFoldersMu.Unlock()
	for ab := range abandonedFolders {
		if ab == key || strings.HasPrefix(key, ab+"/") || strings.HasPrefix(ab, key+"/") {
			delete(abandonedFolders, ab)
		}
	}
}

// isFolderAbandoned 判断文件夹是否已被遗弃（永久，直到 FocusFolder 清除）。
// ★ 前缀匹配：遗弃了父文件夹（用户浏览的目录），其下所有子文件夹的图片
//   做缩略图时也应被拦截——否则切走"截图存档"后，"截图存档/待处理"等子目录
//   的图片仍在读原图制作缩略图（这正是"切换后读盘不停"的根因）。
func isFolderAbandoned(key string) bool {
	if key == "" {
		return false
	}
	abandonedFoldersMu.Lock()
	defer abandonedFoldersMu.Unlock()
	if _, ok := abandonedFolders[key]; ok {
		return true
	}
	for abandoned := range abandonedFolders {
		if strings.HasPrefix(key, abandoned+"/") {
			return true
		}
	}
	return false
}

// imageFolderKey 由完整文件路径推导其所属的文件夹 key（最长前缀匹配已注册根）。
func (a *App) imageFolderKey(filePath string) string {
	if filePath == "" {
		return ""
	}
	norm := strings.ReplaceAll(filePath, "\\", "/")
	a.mu.RLock()
	defer a.mu.RUnlock()
	var bestNorm string
	for r := range a.registeredRoots {
		rn := strings.ReplaceAll(r, "\\", "/")
		if norm == rn || strings.HasPrefix(norm, rn+"/") {
			if len(rn) > len(bestNorm) {
				bestNorm = rn
			}
		}
	}
	if bestNorm == "" {
		return ""
	}
	if norm == bestNorm {
		return bestNorm
	}
	rel := strings.TrimPrefix(norm, bestNorm+"/")
	dir := filepath.Dir(rel)
	// ★ 关键：filepath.Dir 在 Windows 返回反斜杠，必须转回正斜杠，
	//   否则拼接出混合分隔符路径（如 "elsa/截图存档\Coronation Dress截图"），
	//   与 AbandonFolder 标记的正斜杠路径前缀匹配永远失败 → 遗弃检查拦不住子文件夹。
	dir = strings.ReplaceAll(dir, "\\", "/")
	if dir == "." || dir == "" {
		return bestNorm
	}
	return bestNorm + "/" + dir
}

// StopPreGenThumbs 停止预生成
func (a *App) StopPreGenThumbs() map[string]interface{} {
	preGenCloseOnce.Do(func() {
		if preGenCancel != nil {
			close(preGenCancel)
		}
	})
	// 唤醒所有暂停等待的 worker，让它们看到 cancel 信号退出
	atomic.StoreInt32(&preGenPaused, 0)
	preGenCond.Broadcast()
	return map[string]interface{}{"success": true}
}

// PausePreGenThumbs 暂停预生成（幂等）
func (a *App) PausePreGenThumbs() map[string]interface{} {
	atomic.StoreInt32(&preGenPaused, 1)
	preGenStatusMu.Lock()
	preGenStatus.Paused = true
	preGenStatusMu.Unlock()
	return map[string]interface{}{"success": true}
}

// ResumePreGenThumbs 恢复预生成（幂等）
func (a *App) ResumePreGenThumbs() map[string]interface{} {
	atomic.StoreInt32(&preGenPaused, 0)
	preGenCond.Broadcast()
	return map[string]interface{}{"success": true}
}

// GetPreGenStatus 获取当前预生成状态
func (a *App) GetPreGenStatus() *PreGenStatus {
	preGenStatusMu.RLock()
	defer preGenStatusMu.RUnlock()
	s := preGenStatus
	return &s
}

// computeThumbCounts 返回每个文件夹的缩略图缓存数（视频直接算作已完成）
func (a *App) computeThumbCounts() map[string]int {
	thumbCountsMu.Lock()
	defer thumbCountsMu.Unlock()
	if thumbCountsValid {
		return copyCounts(cachedThumbCounts)
	}

	if err := a.ensureThumbDB(); err != nil {
		fmt.Printf("[缩略图] thumbCounts 缓存预热跳过: %v\n", err)
		return nil
	}
	if a.imageDB == nil {
		return nil
	}

	result := make(map[string]int)
	// 1. 从 bbolt 读取所有已缓存缩略图的 ID
	thumbSet := make(map[string]bool)
	a.thumbDB.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(thumbBucket)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			thumbSet[string(k)] = true
		}
		return nil
	})

	// 2. 从 SQLite 读取轻量元数据（id/root_path/folder/is_video），与 thumbSet 做集合运算
	// 不再遍历 a.images（LRU 缓存可能未加载全部）
	metas, err := a.imageDB.LoadImageCacheMetaForThumbCounts()
	if err == nil {
		for _, m := range metas {
			rootPath := strings.ReplaceAll(m.RootPath, "\\", "/")
			folder := strings.ReplaceAll(m.Folder, "\\", "/")
			// 视频直接算作已完成，有缩略图的图片也算
			if m.IsVideo || thumbSet[m.ID] {
				result[rootPath]++
				if folder != "" {
					parts := strings.Split(folder, "/")
					for i := 1; i <= len(parts); i++ {
						subPath := rootPath + "/" + strings.Join(parts[:i], "/")
						result[subPath]++
					}
				}
			}
		}
	}

	cachedThumbCounts = result
	thumbCountsValid = true
	// ★ 持久化到磁盘，供下次冷启动直接恢复（避免每次启动全量扫 8.6GB BoltDB + SQLite）
	a.persistThumbCountsLocked()
	return copyCounts(result)
}

// ==================== thumbCounts 持久化 ====================

// thumbCountsFile 持久化的缩略图计数，附带库文件元数据用于启动时免遍历校验
type thumbCountsFile struct {
	Counts    map[string]int `json:"counts"`
	TotalKeys int            `json:"totalKeys"` // 保存时 BoltDB thumbs bucket 的 key 数
	SavedAt   int64          `json:"savedAt"`
	// ★ 新增：保存时缩略图库文件的大小与修改时间。
	//   启动校验改用这两项（os.Stat，O(1)），不再用 Bucket.Stats() 全树遍历
	//   （488K key / 17GB 库冷缓存下实测 20.9s，是启动慢的元凶）。
	DBSize  int64 `json:"dbSize,omitempty"`
	DBMtime int64 `json:"dbMtime,omitempty"`
}

const thumbCountsFileName = "thumb-counts.json"

// thumbDBKeyCount 返回 BoltDB thumbs bucket 的 key 数；不可用时返回 -1
func (a *App) thumbDBKeyCount() int {
	if a.thumbDB == nil {
		// 启动流程中 thumbDB 在 NewApp 已打开；此处不主动打开，避免锁顺序问题
		return -1
	}
	var n int
	err := a.thumbDB.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(thumbBucket)
		if b == nil {
			return nil
		}
		n = b.Stats().KeyN
		return nil
	})
	if err != nil {
		return -1
	}
	return n
}

// persistThumbCountsLocked 写入磁盘（调用方需已持 thumbCountsMu）
func (a *App) persistThumbCountsLocked() {
	counts := copyCounts(cachedThumbCounts)
	keyCount := a.thumbDBKeyCount()
	if keyCount < 0 || a.userDataDir == "" {
		return
	}
	// ★ 同时记录库文件元数据（大小+修改时间），供冷启动免遍历校验
	var dbSize, dbMtime int64
	if info, err := os.Stat(a.GetThumbDir()); err == nil {
		dbSize = info.Size()
		dbMtime = info.ModTime().Unix()
	}
	data := thumbCountsFile{Counts: counts, TotalKeys: keyCount, SavedAt: time.Now().Unix(), DBSize: dbSize, DBMtime: dbMtime}
	if b, err := json.Marshal(data); err == nil {
		path := filepath.Join(a.userDataDir, thumbCountsFileName)
		tmp := path + ".tmp"
		if os.WriteFile(tmp, b, 0644) == nil {
			os.Rename(tmp, path)
		}
	}
}

// persistThumbCounts 供关闭流程调用（自持锁）
func (a *App) persistThumbCounts() {
	thumbCountsMu.RLock()
	defer thumbCountsMu.RUnlock()
	if !thumbCountsValid {
		return
	}
	a.persistThumbCountsLocked()
}

// loadThumbCountsFromDisk 启动时尝试恢复 thumbCounts。
// 仅当缩略图库自保存以来无变化才采用，否则触发重算。
func (a *App) loadThumbCountsFromDisk() {
	if a.userDataDir == "" {
		return
	}
	path := filepath.Join(a.userDataDir, thumbCountsFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var f thumbCountsFile
	if err := json.Unmarshal(data, &f); err != nil || f.Counts == nil {
		return
	}
	// ★ 免遍历校验：用库文件的大小+修改时间判断缩略图库是否有变化。
	//   原来用 Bucket.Stats().KeyN 对比 key 数——Stats() 会遍历整个 B 树（读全部页），
	//   488K key / 17GB 库冷缓存下实测 20.9s，是启动慢的元凶。
	dbChanged := true
	if info, err := os.Stat(a.GetThumbDir()); err == nil {
		if f.DBSize != 0 || f.DBMtime != 0 {
			dbChanged = info.Size() != f.DBSize || info.ModTime().Unix() != f.DBMtime
		} else {
			// 旧格式缓存（升级前写入，无 DB 元数据）→ 无法校验，直接采用；
			// 该文件由上次关闭流程写入，通常就是当前库，下次保存会补上新字段。
			fmt.Printf("[缩略图] 旧格式 thumbCounts（无 DB 元数据）直接采用，下次保存将写入新字段\n")
			dbChanged = false
		}
	}
	if dbChanged {
		fmt.Printf("[缩略图] 缩略图库有变化（文件元数据与保存时不一致），跳过缓存的 thumbCounts，稍后重算\n")
		return
	}
	thumbCountsMu.Lock()
	cachedThumbCounts = f.Counts
	thumbCountsValid = true
	thumbCountsMu.Unlock()
	fmt.Printf("[缩略图] 从磁盘恢复 thumbCounts（%d 个文件夹）\n", len(f.Counts))
}

func copyCounts(src map[string]int) map[string]int {
	dst := make(map[string]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// getCachedThumbCounts 读取缓存的 thumbCounts，不触发计算。nil 表示缓存未就绪。
func (a *App) getCachedThumbCounts() map[string]int {
	thumbCountsMu.RLock()
	defer thumbCountsMu.RUnlock()
	if !thumbCountsValid {
		return nil
	}
	return copyCounts(cachedThumbCounts)
}

// PreloadThumbCounts 预热 thumbCounts 缓存（启动时异步调用，避免 GetFolders 首次阻塞）
func (a *App) PreloadThumbCounts() {
	if a.computeThumbCounts() == nil {
		return
	}
	fmt.Println("[启动] thumbCounts 缓存预热完成")
	if a.ctx != nil {
		a.emitThumbProgress()
	}
}

// thumbPreloadDelay 预热 thumbCounts 的延迟：让冷启动的首次渲染/索引加载先完成，
// 不与首屏抢磁盘/CPU。完成后通过 thumb:progress 事件通知前端刷新计数。
const thumbPreloadDelay = 8 * time.Second

var (
	thumbPreloadMu    sync.Mutex
	thumbPreloadTimer *time.Timer
)

// scheduleThumbCountPreload 延迟调度 thumbCounts 预热（可重排，运行期缓存失效后也会重新触发）。
func (a *App) scheduleThumbCountPreload() {
	thumbPreloadMu.Lock()
	defer thumbPreloadMu.Unlock()
	if thumbPreloadTimer != nil {
		return // 已排定，避免重复
	}
	thumbPreloadTimer = time.AfterFunc(thumbPreloadDelay, func() {
		a.PreloadThumbCounts()
		thumbPreloadMu.Lock()
		thumbPreloadTimer = nil
		thumbPreloadMu.Unlock()
	})
}

// invalidateThumbCounts 标记缓存失效并通知前端（仅在删除缩略图时使用）
func (a *App) invalidateThumbCounts() {
	thumbCountsMu.Lock()
	thumbCountsValid = false
	thumbCountsMu.Unlock()
	a.throttleThumbProgress()
}

// incrementThumbCount 增量更新：给 imageID 所属文件夹（含所有上级）的缩略图计数 +1
func (a *App) incrementThumbCount(imageID string) {
	a.mu.RLock()
	entry, ok := a.images[imageID]
	a.mu.RUnlock()
	if !ok {
		// LRU 未命中：回退 SQL 单点查询（缩略图刚生成时图片可能未在缓存）
		if a.imageDB != nil {
			if e, err := a.imageDB.GetImageEntry(imageID); err == nil && e != nil {
				entry = &ImageEntry{
					ID: e.ID, Path: e.Path, Name: e.Name, Size: e.Size,
					LastModified: e.LastModified, CreatedAt: e.CreatedAt,
					Folder: e.Folder, RootPath: e.RootPath,
					Width: e.Width, Height: e.Height, IsVideo: e.IsVideo,
					URL: fmt.Sprintf("/image/%s", e.ID),
				}
				ok = true
			}
		}
	}
	if !ok || entry.IsVideo {
		return
	}
	rootPath := strings.ReplaceAll(entry.RootPath, "\\", "/")
	folder := strings.ReplaceAll(entry.Folder, "\\", "/")

	thumbCountsMu.Lock()
	if !thumbCountsValid {
		thumbCountsMu.Unlock()
		return
	}
	// 记录本次变化的文件夹新计数（含所有上级），供 thumb:progress 增量定向通知
	updated := map[string]int{}
	cachedThumbCounts[rootPath]++
	updated[rootPath] = cachedThumbCounts[rootPath]
	if folder != "" {
		parts := strings.Split(folder, "/")
		for i := 1; i <= len(parts); i++ {
			subPath := rootPath + "/" + strings.Join(parts[:i], "/")
			cachedThumbCounts[subPath]++
			updated[subPath] = cachedThumbCounts[subPath]
		}
	}
	thumbCountsMu.Unlock()

	// 合并进通知缓冲
	thumbNotifyMu.Lock()
	if thumbFolderCounts == nil {
		thumbFolderCounts = make(map[string]int)
	}
	for k, v := range updated {
		thumbFolderCounts[k] = v
	}
	thumbNotifyMu.Unlock()

	a.throttleThumbProgress()
}

// takeThumbFolderCountsLocked 取出并清空待通知的文件夹缩略图计数（调用方需持有 thumbNotifyMu）
func takeThumbFolderCountsLocked() map[string]int {
	if len(thumbFolderCounts) == 0 {
		return nil
	}
	folders := thumbFolderCounts
	thumbFolderCounts = nil
	return folders
}

// emitThumbProgressPayload 发送 thumb:progress 事件，携带本次变化的文件夹计数（前端据此定向更新徽章）
func (a *App) emitThumbProgressPayload() {
	payload := map[string]interface{}{}
	if folders := takeThumbFolderCountsLocked(); len(folders) > 0 {
		payload["folders"] = folders
	}
	wailsruntime.EventsEmit(a.ctx, "thumb:progress", payload)
}

// throttleThumbProgress 节流通知前端：首次立即发射，冷却期内置脏标记，冷却结束补发
func (a *App) throttleThumbProgress() {
	thumbNotifyMu.Lock()
	if thumbNotifyTimer != nil {
		thumbNotifyDirty = true // 冷却期内有新的增量，标记需要补发
		thumbNotifyMu.Unlock()
		return
	}
	thumbNotifyDirty = false
	// 立即发射（携带已累计的文件夹计数）
	a.emitThumbProgressPayload()
	thumbNotifyTimer = time.AfterFunc(thumbNotifyThrottle, func() {
		thumbNotifyMu.Lock()
		if thumbNotifyDirty {
			thumbNotifyDirty = false
			a.emitThumbProgressPayload()
		}
		thumbNotifyTimer = nil
		thumbNotifyMu.Unlock()
	})
	thumbNotifyMu.Unlock()
}

// emitThumbProgress 立即通知前端刷新（预生成定时器/完成时使用，不走防抖）
func (a *App) emitThumbProgress() {
	thumbNotifyMu.Lock()
	a.emitThumbProgressPayload()
	thumbNotifyMu.Unlock()
}

// CleanOrphanedThumbs 清理 BoltDB 中孤立缩略图（没有对应 images 记录的 key）
func (a *App) CleanOrphanedThumbs() map[string]interface{} {
	if a.thumbDB == nil {
		return map[string]interface{}{"success": false, "error": "BoltDB 未初始化"}
	}

	// ★ 从 SQL 读取所有 imageID，不再依赖 a.images（LRU 可能未加载全部）
	validIDs := make(map[string]bool)
	if a.imageDB != nil {
		if ids, err := a.imageDB.LoadImageCacheIDs(); err == nil {
			for _, id := range ids {
				validIDs[id] = true
			}
		}
	}

	var cleaned int
	a.thumbDB.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(thumbBucket)
		var keysToDelete [][]byte
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			if !validIDs[string(k)] {
				keysToDelete = append(keysToDelete, k)
			}
		}
		for _, k := range keysToDelete {
			b.Delete(k)
			cleaned++
		}
		return nil
	})

	if cleaned > 0 {
		thumbGeneration.Add(1)
		fmt.Printf("[缩略图] 清理了 %d 个孤立缓存\n", cleaned)
	}

	a.cleanThumbGenLocks()

	return map[string]interface{}{"success": true, "cleaned": cleaned}
}

// cleanThumbGenLocks 移除 thumbGenLocks 中不属于当前 images 的孤立锁，防止 sync.Map 无限增长
func (a *App) cleanThumbGenLocks() {
	// ★ 从 SQL 读取所有 imageID，不再依赖 a.images
	validIDs := make(map[string]bool)
	if a.imageDB != nil {
		if ids, err := a.imageDB.LoadImageCacheIDs(); err == nil {
			for _, id := range ids {
				validIDs[id] = true
			}
		}
	}

	thumbGenLocks.Range(func(key, value interface{}) bool {
		if id, ok := key.(string); ok && !validIDs[id] {
			thumbGenLocks.Delete(id)
		}
		return true
	})
}

// removeThumbsByIDs 根据图片ID列表从 BoltDB 删除缩略图
func (a *App) removeThumbsByIDs(ids []string) {
	if len(ids) == 0 || a.thumbDB == nil {
		return
	}
	// ★ 分批提交 bbolt 事务：单事务删几万条会长期持有 bbolt 写锁，
	//   阻塞缩略图生成/读取。分批让锁周期性释放，其它操作可穿插。
	const batchSize = 1000
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		a.thumbDB.Update(func(tx *bbolt.Tx) error {
			b := tx.Bucket(thumbBucket)
			for _, id := range chunk {
				b.Delete([]byte(id))
				thumbMemRemove(id) // 同步清内存缓存
			}
			return nil
		})
	}
}

// ClearFolderThumbs 清除指定文件夹（含子文件夹）的缩略图缓存
func (a *App) ClearFolderThumbs(folderPath string) map[string]interface{} {
	if folderPath == "" {
		return map[string]interface{}{"success": false, "error": "请提供文件夹路径"}
	}

	a.mu.RLock()
	normalizedInput := strings.ToLower(strings.ReplaceAll(folderPath, "\\", "/"))
	var idsToRemove []string
	var matchedFolders int
	for folderKey, ids := range a.folderIndex {
		normalizedKey := strings.ToLower(folderKey)
		if normalizedKey == normalizedInput || strings.HasPrefix(normalizedKey, normalizedInput+"/") {
			idsToRemove = append(idsToRemove, ids...)
			matchedFolders++
		}
	}
	a.mu.RUnlock()

	if len(idsToRemove) == 0 {
		return map[string]interface{}{"success": true, "cleaned": 0, "message": "该文件夹无缩略图缓存"}
	}

	a.removeThumbsByIDs(idsToRemove)
	thumbGeneration.Add(1)

	fmt.Printf("[缩略图] 已清除文件夹 %s (%d个子目录) 的 %d 个缩略图\n", folderPath, matchedFolders, len(idsToRemove))
	return map[string]interface{}{"success": true, "cleaned": len(idsToRemove), "folderCount": matchedFolders}
}

// GetUserDataDir 返回当前用户数据目录
func (a *App) GetUserDataDir() string {
	fmt.Printf("[用户数据] GetUserDataDir 返回: %s\n", a.userDataDir)
	return a.userDataDir
}

// saveUserDataDirToDefault 把自定义目录路径写入 exe 根目录的 .gallery-userdir 文件
// ==================== 后台暂停 / 恢复 ====================

// PauseBackground 暂停后台扫描和索引（供前端在打开设置时调用）
func (a *App) PauseBackground() {
	a.bgPaused.Store(1)
	// 等待正在进行的扫描完成
	a.scanMu.Lock()
	a.scanMu.Unlock()
	fmt.Println("[后台] 已暂停")
}

// ResumeBackground 恢复后台扫描和索引
func (a *App) ResumeBackground() {
	a.bgPaused.Store(0)
	fmt.Println("[后台] 已恢复")
}

// RestartWithNewPaths 执行热重启：关旧 DB → 开新 DB → 加载数据 → 原子替换
// 路径从 .gallery-userdir 和 .gallery-settings.json 读取（这是唯一的数据源）
func (a *App) RestartWithNewPaths() map[string]interface{} {
	// 防止重入：如果已有切换在进行中，直接返回
	if !a.switchMu.TryLock() {
		return map[string]interface{}{"success": false, "error": "切换正在进行中，请稍后重试"}
	}
	defer a.switchMu.Unlock()
	// ★ 切换期间暂停后台，无论成功/失败都必须恢复，否则扫描/预生成一直停摆。
	defer a.bgPaused.Store(0)

	execDir, _ := os.Getwd()
	defaultDir := filepath.Join(execDir, "user")

	// 优先从内存读取（SetUserDataDir 设定的待切换路径），再回退到文件
	newUserDir := a.pendingUserDir
	if newUserDir == "" {
		newUserDir = defaultDir
		if bytes, err := os.ReadFile(filepath.Join(execDir, ".gallery-userdir")); err == nil {
			saved := strings.TrimSpace(string(bytes))
			if saved != "" {
				if info, err := os.Stat(saved); err == nil && info.IsDir() {
					newUserDir = saved
				}
			}
		}
	}
	a.pendingUserDir = "" // 清除，防止下次误用

	userDataFile := filepath.Join(newUserDir, "user-data.json")
	// 新目录：创建空 user-data.json 以完成初始化
	if _, err := os.Stat(userDataFile); err != nil {
		os.MkdirAll(newUserDir, 0755)
		emptyData := map[string]interface{}{"settings": map[string]interface{}{}}
		if data, err := json.MarshalIndent(emptyData, "", "  "); err == nil {
			os.WriteFile(userDataFile, data, 0644)
		}
	}

	var newThumbDBPath string
	settings := readGlobalSettings()
	if dir, ok := settings["thumbDir"].(string); ok && dir != "" {
		if filepath.Ext(dir) == ".db" {
			newThumbDBPath = dir
		} else {
			newThumbDBPath = filepath.Join(dir, "thumbnails.db")
		}
	}
	if newThumbDBPath == "" {
		newThumbDBPath = filepath.Join(newUserDir, "thumbnails.db")
	}

	fmt.Printf("[热切换] 目标 userDir: %s, thumbDB: %s\n", newUserDir, newThumbDBPath)

	// ★ 不调用 saveImageIndex()：正常扫描流程已定期保存，切换时无需重复序列化 44 万条 JSON

	// 0. 取消所有活跃任务，防止旧 goroutine 写入新数据库
	a.indexingRootsMu.Lock()
	for root, cancel := range a.indexingRoots {
		cancel()
		delete(a.indexingRoots, root)
	}
	a.indexingRootsMu.Unlock()

	a.StopPreGenThumbs()
	a.bgPaused.Store(1)

	// 等待正在进行的扫描释放 scanMu，然后清空标记，防止旧 goroutine 的 defer 误删新任务标记
	a.scanMu.Lock()
	for k := range a.scanningRoots {
		delete(a.scanningRoots, k)
	}
	a.scanMu.Unlock()

	// 1. thumbDB 延迟到新库打开成功后再原子替换，避免失败时进入半切换状态
	a.thumbDBMu.Lock()

	// 2. 后台 goroutine：打开新 SQLite + 加载图片缓存（最耗时操作）
	type loadResult struct {
		db        *database.ImageDB
		dbErr     error
		images    map[string]*ImageEntry
		folderIdx map[string][]string
	}
	ch := make(chan loadResult, 1)
	go func() {
		r := loadResult{}
		r.db, r.dbErr = database.New(filepath.Join(newUserDir, "images.db"))
		if r.dbErr != nil {
			ch <- r
			return
		}
		r.images = make(map[string]*ImageEntry)
		r.folderIdx = make(map[string][]string)
		if records, err := r.db.LoadAllImageCache(); err == nil {
			for _, rec := range records {
				r.images[rec.ID] = &ImageEntry{
					ID:           rec.ID,
					Path:         rec.Path,
					Name:         rec.Name,
					Size:         rec.Size,
					LastModified: rec.LastModified,
					CreatedAt:    rec.CreatedAt,
					Folder:       rec.Folder,
					RootPath:     rec.RootPath,
					URL:          fmt.Sprintf("/image/%s", rec.ID),
					Width:        rec.Width,
					Height:       rec.Height,
					IsVideo:      rec.IsVideo,
				}
				rootNorm := strings.ReplaceAll(rec.RootPath, "\\", "/")
				folderKey := rootNorm
				if rec.Folder != "" {
					folderKey = rootNorm + "/" + strings.ReplaceAll(rec.Folder, "\\", "/")
				}
				r.folderIdx[folderKey] = append(r.folderIdx[folderKey], rec.ID)
			}
		}
		ch <- r
	}()

	// 3. 主线程：加载 user-data + 打开 BoltDB（与 SQLite 加载并行）
	r := <-ch
	if r.dbErr != nil {
		fmt.Printf("[热切换] 打开新 SQLite 失败: %v\n", r.dbErr)
		a.thumbDBMu.Unlock()
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("无法打开新数据库: %v", r.dbErr)}
	}

	newFolderCount := make(map[string]int, len(r.folderIdx))
	for k, v := range r.folderIdx {
		newFolderCount[k] = len(v)
	}

	newRegisteredRoots := make(map[string]bool)
	newFolderTypes := make(map[string]string)
	// 优先从 SQLite 加载导入根目录
	if newUserDataDB, err := database.NewUserDataDB(filepath.Join(newUserDir, "user-data.db")); err == nil {
		if dbRoots, err := newUserDataDB.GetAllRoots(); err == nil {
			for _, r := range dbRoots {
				newRegisteredRoots[r.Path] = true
				if r.FolderType != "" {
					newFolderTypes[r.Path] = r.FolderType
				}
			}
		}
		newUserDataDB.Close()
	}
	// fallback: 从 JSON 加载
	if len(newRegisteredRoots) == 0 {
		if userData := readJSONFile(userDataFile); userData != nil {
			if roots, ok := userData["registeredRoots"].([]interface{}); ok {
				for _, root := range roots {
					if rs, ok := root.(string); ok {
						newRegisteredRoots[rs] = true
					}
				}
			}
			if types, ok := userData["folderTypes"].(map[string]interface{}); ok {
				for k, v := range types {
					if vs, ok := v.(string); ok {
						newFolderTypes[k] = vs
					}
				}
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(newThumbDBPath), 0755); err != nil {
		a.thumbDBMu.Unlock()
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("无法创建缩略图目录: %v", err)}
	}
	time.Sleep(50 * time.Millisecond)
	newThumb, err := bbolt.Open(newThumbDBPath, 0644, &bbolt.Options{Timeout: 3 * time.Second, NoSync: true})
	if err != nil {
		a.thumbDBMu.Unlock()
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("无法打开缩略图数据库: %v", err)}
	}
	if err := newThumb.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(thumbBucket)
		return err
	}); err != nil {
		newThumb.Close()
		a.thumbDBMu.Unlock()
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("无法初始化缩略图数据库: %v", err)}
	}

	fmt.Printf("[热切换] 已加载: images=%d, folders=%d, roots=%d\n",
		len(r.images), len(r.folderIdx), len(newRegisteredRoots))

	// 4. 原子替换
	a.mu.Lock()
	oldDB := a.imageDB
	oldUserDataDB := a.userDataDB
	oldThumb := a.thumbDB
	a.userDataDir = newUserDir
	a.userDataFile = userDataFile
	a.windowStateFile = filepath.Join(newUserDir, "window-state.json")

	a.imageDB = r.db
	a.userDataDB, _ = database.NewUserDataDB(filepath.Join(newUserDir, "user-data.db"))
	a.thumbDB = newThumb

	a.images = r.images
	a.folderIndex = r.folderIdx
	a.folderCount = newFolderCount
	a.registeredRoots = newRegisteredRoots
	a.folderTypes = newFolderTypes
	a.mu.Unlock()
	if oldDB != nil {
		oldDB.Close()
	}
	if oldUserDataDB != nil {
		oldUserDataDB.Close()
	}
	if oldThumb != nil {
		oldThumb.Close()
	}
	a.thumbDBMu.Unlock()

	// 清理旧数据的缓存，避免前端短暂看到旧数据
	a.cleanThumbGenLocks()
	thumbCountsMu.Lock()
	thumbCountsValid = false
	cachedThumbCounts = nil
	thumbCountsMu.Unlock()
	thumbGeneration.Add(1)

	fmt.Printf("[热切换] 完成: userDir=%s, images=%d\n", newUserDir, len(r.images))
	return map[string]interface{}{"success": true, "userDataDir": newUserDir}
}

// SetUserDataDir 仅保存新路径到文件和内存，不执行重启（由 RestartWithNewPaths 统一执行）
func (a *App) SetUserDataDir(path string) map[string]interface{} {
	if path == "" {
		path = a.defaultUserDataDir
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return map[string]interface{}{"success": false, "error": "路径解析失败: " + err.Error()}
	}
	os.MkdirAll(resolved, 0755)
	a.saveUserDataDirToDefault(resolved)
	a.pendingUserDir = resolved // 内存标记，供 RestartWithNewPaths 优先读取
	fmt.Printf("[用户数据] 待切换路径已保存: %s\n", resolved)
	return map[string]interface{}{"success": true, "userDataDir": resolved, "message": "路径已保存（关闭设置后生效）"}
}

// SetThumbDir 仅保存路径到全局设置，不立即切换（由 RestartWithNewPaths 统一执行）
func (a *App) SetThumbDir(path string) map[string]interface{} {
	if path != "" {
		resolved, err := filepath.Abs(path)
		if err != nil {
			return map[string]interface{}{"success": false, "error": "路径解析失败: " + err.Error()}
		}
		path = resolved
	}
	current := readGlobalSettings()
	if path == "" {
		delete(current, "thumbDir")
	} else {
		current["thumbDir"] = path
	}
	if err := writeGlobalSettings(current); err != nil {
		return map[string]interface{}{"success": false, "error": "保存设置失败: " + err.Error()}
	}
	// ★ 防乌龙：检查目标缩略图库状态，明确提示用户将指向什么。
	//   避免误指向空目录后重启，出现"数量 0 + 全量重新生成"的困惑。
	//   thumbDir 支持两种形式：文件夹（自动补 thumbnails.db）或 .db 文件直接指定。
	msg := "路径已保存（重启后生效）"
	if path != "" {
		targetPath := path
		if filepath.Ext(path) != ".db" {
			targetPath = filepath.Join(path, "thumbnails.db")
		}
		if targetPath != a.GetThumbDir() { // 目标与当前打开的库不同才统计
			if n := countThumbKeysInFile(targetPath); n < 0 {
				msg = "路径已保存（重启后生效）。注意：目标位置没有缩略图库，切换后将从空库开始，已有缩略图不会丢失但需要时重新生成"
			} else if n == 0 {
				msg = "路径已保存（重启后生效）。注意：目标库为空（0 张缩略图）"
			} else {
				msg = fmt.Sprintf("路径已保存（重启后生效）。目标库已有 %d 张缩略图", n)
			}
		}
	}
	return map[string]interface{}{"success": true, "thumbDir": path, "message": msg}
}

// countThumbKeysInFile 只读统计指定 BoltDB 文件的缩略图 key 数；文件不存在/无法打开返回 -1
func countThumbKeysInFile(path string) int {
	if path == "" {
		return -1
	}
	db, err := bbolt.Open(path, 0600, &bbolt.Options{ReadOnly: true, Timeout: 2 * time.Second})
	if err != nil {
		return -1
	}
	defer db.Close()
	var n int
	_ = db.View(func(tx *bbolt.Tx) error {
		if b := tx.Bucket(thumbBucket); b != nil {
			n = b.Stats().KeyN
		}
		return nil
	})
	return n
}
func (a *App) saveUserDataDirToDefault(userDataDir string) {
	execDir, err := os.Getwd()
	if err != nil {
		return
	}
	realDefault := filepath.Join(execDir, "user")
	redirectFile := filepath.Join(execDir, ".gallery-userdir")
	if userDataDir == realDefault {
		os.Remove(redirectFile) // 切换回默认目录时清除重定向
		return
	}
	if err := os.WriteFile(redirectFile, []byte(userDataDir), 0644); err != nil {
		fmt.Printf("[用户数据] 写入重定向文件失败: %v\n", err)
	}
}

// applySavedUserDataDir 启动时检查 .gallery-userdir 重定向文件，有则切换到自定义目录
func (a *App) applySavedUserDataDir() {
	execDir, err := os.Getwd()
	if err != nil {
		return
	}
	redirectFile := filepath.Join(execDir, ".gallery-userdir")
	bytes, err := os.ReadFile(redirectFile)
	if err != nil {
		return // 无重定向文件，使用默认目录
	}
	saved := strings.TrimSpace(string(bytes))
	if saved == "" || saved == a.userDataDir {
		return
	}
	// 验证目标目录存在且可读写
	if info, err := os.Stat(saved); err != nil || !info.IsDir() {
		fmt.Printf("[用户数据] 已保存的目录不存在，忽略并清除重定向: %s\n", saved)
		os.Remove(redirectFile)
		return
	}
	// 验证目标目录下 user-data.json 可读（确保目录正确）
	userDataFile := filepath.Join(saved, "user-data.json")
	if _, err := os.Stat(userDataFile); err != nil {
		fmt.Printf("[用户数据] 目标目录缺少 user-data.json，忽略并清除重定向: %s\n", saved)
		os.Remove(redirectFile)
		return
	}
	fmt.Printf("[用户数据] 恢复为上次设置的目录: %s\n", saved)
	a.userDataDir = saved
	a.userDataFile = userDataFile
	a.windowStateFile = filepath.Join(saved, "window-state.json")
}

func readJSONFile(path string) map[string]interface{} {
	data := make(map[string]interface{})
	bytes, err := os.ReadFile(path)
	if err != nil {
		return data
	}
	json.Unmarshal(bytes, &data)
	return data
}

func writeJSONFile(path string, data map[string]interface{}) {
	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(path, bytes, 0644)
}

// GetThumbCacheInfo 返回缩略图缓存统计信息（BoltDB 单文件）
func (a *App) GetThumbCacheInfo() map[string]interface{} {
	if a.thumbDB == nil {
		return map[string]interface{}{"count": 0, "totalSize": int64(0), "totalSizeStr": "0 B", "dir": a.GetThumbDir()}
	}

	var count int
	var dbFileSize int64

	// 获取 BoltDB 文件大小（★ 统计实际打开的库，而非 userDataDir 下的同名文件——
	//   thumbDir 可配置到其它目录，读错文件会出现"数量 0 占用 10GB"的矛盾显示）
	dbPath := a.GetThumbDir()
	if dbPath != "" {
		if info, err := os.Stat(dbPath); err == nil {
			dbFileSize = info.Size()
		}
	}

	// 统计 key 数量
	a.thumbDB.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(thumbBucket)
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			count++
		}
		return nil
	})

	return map[string]interface{}{
		"count":        count,
		"totalSize":    dbFileSize,
		"totalSizeStr": formatThumbSize(dbFileSize),
		"dir":          a.GetThumbDir(),
	}
}

func formatThumbSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

// createVideoPlaceholderJPEG 生成视频占位缩略图（深色背景 JPEG）
func createVideoPlaceholderJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 500, 500))
	for y := 0; y < 500; y++ {
		for x := 0; x < 500; x++ {
			img.Set(x, y, color.RGBA{30, 30, 30, 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 60})
	return buf.Bytes()
}

// GetImageDimensionsVips 用 govips 读取图片宽高（支持 AVIF，考虑 EXIF 旋转）
func GetImageDimensionsVips(filePath string) (int, int) {
	img, err := vips.NewImageFromFile(filePath)
	if err != nil {
		return 0, 0
	}
	defer img.Close()
	w, h := img.Width(), img.Height()
	// 读取 EXIF Orientation，如果图片需要旋转 90° 或 270°，交换宽高
	if orientation := readEXIFOrientationVips(img); orientation >= 5 && orientation <= 8 {
		w, h = h, w
	}
	return w, h
}

// readEXIFOrientationVips 从 vips 图片对象读取 EXIF Orientation 标签
func readEXIFOrientationVips(img *vips.ImageRef) int {
	val := img.GetInt("exif-ifd0-Orientation")
	return val
}
