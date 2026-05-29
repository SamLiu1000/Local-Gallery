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
)

var thumbBucket = []byte("thumbs")

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

// generateThumbnail 用 libvips thumbnail API 生成 JPEG 缩略图（shrink-on-load），写入 BoltDB
func (a *App) generateThumbnail(srcPath string, imageID string) error {
	if a.thumbDB == nil {
		return fmt.Errorf("BoltDB 未初始化")
	}

	// 跳过空路径
	if srcPath == "" {
		return fmt.Errorf("图片路径为空: %s", imageID)
	}

	// 跳过 0 字节文件，防止 vips 崩溃
	if info, err := os.Stat(srcPath); err != nil {
		// 文件不存在或无法访问，标记失败并返回
		a.markThumbFailed(imageID)
		return fmt.Errorf("文件不存在或无法访问: %s", srcPath)
	} else if info.Size() == 0 {
		a.markThumbFailed(imageID)
		return fmt.Errorf("文件为空: %s", srcPath)
	}

	// 视频文件：存储黑色占位 JPEG（无需帧提取）
	if isVideoFile(filepath.Base(srcPath)) {
		placeholder := createVideoPlaceholderJPEG()
		return a.thumbDB.Update(func(tx *bbolt.Tx) error {
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

	img, err := vips.NewThumbnailFromFile(srcPath, thumbMaxSize, thumbMaxSize, vips.InterestingNone)
	if err != nil {
		a.markThumbFailed(imageID)
		return fmt.Errorf("vips 缩略图生成失败: %w", err)
	}
	defer img.Close()

	ep := vips.NewJpegExportParams()
	ep.Quality = thumbJPEGQuality
	ep.StripMetadata = true

	jpegBytes, _, err := img.ExportJpeg(ep)
	if err != nil {
		a.markThumbFailed(imageID)
		return fmt.Errorf("vips 导出 JPEG 失败: %w", err)
	}

	// 写入 BoltDB
	err = a.thumbDB.Update(func(tx *bbolt.Tx) error {
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
	if header[0] == 0xFF && header[1] == 0xD8 && header[2] == 0xFF { return true } // JPEG
	if header[0] == 0x89 && header[1] == 0x50 && header[2] == 0x4E && header[3] == 0x47 { return true } // PNG
	if header[0] == 0x47 && header[1] == 0x49 && header[2] == 0x46 && header[3] == 0x38 { return true } // GIF
	if header[0] == 0x42 && header[1] == 0x4D && n >= 6 { // BMP
		// BMP 文件头 14B: 2B魔数(BM) + 4B文件大小 + 4B保留(必须为0) + 4B偏移
		// 保留字段必须为 0，防止非 BMP 文件误入导致 libvips C 层崩溃
		reserved := uint32(header[6]) | uint32(header[7])<<8 | uint32(header[8])<<16 | uint32(header[9])<<24
		return reserved == 0
	}
	if header[0] == 0x52 && header[1] == 0x49 && header[2] == 0x46 && header[3] == 0x46 &&
		n >= 12 && header[8] == 0x57 && header[9] == 0x45 && header[10] == 0x42 && header[11] == 0x50 { return true } // WebP
	if header[0] == 0x49 && header[1] == 0x49 && header[2] == 0x2A && header[3] == 0x00 { return true } // TIFF LE
	if header[0] == 0x4D && header[1] == 0x4D && header[2] == 0x00 && header[3] == 0x2A { return true } // TIFF BE
	if header[0] == 0x00 && header[1] == 0x00 && header[2] == 0x01 && header[3] == 0x00 { return true } // ICO
	if header[0] == '<' { return true } // SVG/XML
	if n >= 8 && header[4] == 0x66 && header[5] == 0x74 && header[6] == 0x79 && header[7] == 0x70 { return true } // HEIC/AVIF

	// 已知非图片魔数 → 拦截（伪装扩展名的损坏文件）
	// TGA: 文件尾有 TRUEVISION 或开头并非 JPEG
	// RIFF 非 WebP: AVI/WAV 等
	if header[0] == 0x52 && header[1] == 0x49 && header[2] == 0x46 && header[3] == 0x46 { return false } // RIFF 但非 WebP
	// EXE/DLL: MZ
	if header[0] == 0x4D && header[1] == 0x5A { return false }
	// ZIP/DOCX/XLSX: PK
	if header[0] == 0x50 && header[1] == 0x4B { return false }
	// PDF
	if header[0] == 0x25 && header[1] == 0x50 && header[2] == 0x44 && header[3] == 0x46 { return false }
	// RAR
	if header[0] == 0x52 && header[1] == 0x61 && header[2] == 0x72 && header[3] == 0x21 { return false }
	// 7z
	if header[0] == 0x37 && header[1] == 0x7A && header[2] == 0xBC && header[3] == 0xAF { return false }
	// GZIP
	if header[0] == 0x1F && header[1] == 0x8B { return false }

	// 无法确认 → 信任扩展名放行（相机 RAW/CR2/NEF/ORF 等无法通过魔数确认）
	return true
}

// serveThumbnail 从 BoltDB 读取缩略图 JPEG 字节；如不存在则生成
func (a *App) serveThumbnail(imageID string) ([]byte, error) {
	imagePath := a.resolveImagePath(imageID)
	if imagePath == "" {
		a.markThumbFailed(imageID)
		return nil, fmt.Errorf("图片未找到: %s", imageID)
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
			return jpegBytes, nil
		}
	}

	// 信号量限制并发数
	release := thumbSemAcquire()
	defer release()

	if err := a.generateThumbnail(imagePath, imageID); err != nil {
		fmt.Printf("[缩略图] 生成失败 %s: %v\n", imageID, err)
		a.markThumbFailed(imageID)
		return nil, err
	}

	// 读取刚写入的数据
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
)

const thumbNotifyThrottle = 300 * time.Millisecond

// StartPreGenThumbs 开始后台预生成缩略图（按多个文件夹）
func (a *App) StartPreGenThumbs(folders []string) map[string]interface{} {
	// 规范化路径分隔符，与 folderIndex / folderCount 的 key 保持一致
	normFolders := make([]string, len(folders))
	for i, f := range folders {
		normFolders[i] = strings.ReplaceAll(f, "\\", "/")
	}

	// 收集所有选中文件夹及其子文件夹中的图片，按 ID 去重
	seen := make(map[string]bool)
	var entries []*ImageEntry
	a.mu.RLock()
	for _, folder := range normFolders {
		prefix := folder + "/"
		for k, ids := range a.folderIndex {
			if k == folder || strings.HasPrefix(k, prefix) {
				for _, id := range ids {
					if !seen[id] {
						seen[id] = true
						if entry := a.images[id]; entry != nil {
							entries = append(entries, entry)
						}
					}
				}
			}
		}
	}
	a.mu.RUnlock()

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

	result := make(map[string]int)
	if a.thumbDB != nil {
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

		a.mu.RLock()
		for _, entry := range a.images {
			rootPath := strings.ReplaceAll(entry.RootPath, "\\", "/")
			folder := strings.ReplaceAll(entry.Folder, "\\", "/")
			// 视频直接算作已完成，有缩略图的图片也算
			if entry.IsVideo || thumbSet[entry.ID] {
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
		a.mu.RUnlock()
	}

	cachedThumbCounts = result
	thumbCountsValid = true
	return copyCounts(result)
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
	a.computeThumbCounts()
	fmt.Println("[启动] thumbCounts 缓存预热完成")
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
	cachedThumbCounts[rootPath]++
	if folder != "" {
		parts := strings.Split(folder, "/")
		for i := 1; i <= len(parts); i++ {
			subPath := rootPath + "/" + strings.Join(parts[:i], "/")
			cachedThumbCounts[subPath]++
		}
	}
	thumbCountsMu.Unlock()

	a.throttleThumbProgress()
}

// throttleThumbProgress 节流通知前端：首次立即发射，冷却期内置脏标记，冷却结束补发
func (a *App) throttleThumbProgress() {
	thumbNotifyMu.Lock()
	if thumbNotifyTimer != nil {
		thumbNotifyDirty = true // 冷却期内有新的增量，标记需要补发
		thumbNotifyMu.Unlock()
		return
	}
	wailsruntime.EventsEmit(a.ctx, "thumb:progress", map[string]interface{}{})
	thumbNotifyDirty = false
	thumbNotifyTimer = time.AfterFunc(thumbNotifyThrottle, func() {
		thumbNotifyMu.Lock()
		if thumbNotifyDirty {
			wailsruntime.EventsEmit(a.ctx, "thumb:progress", map[string]interface{}{})
		}
		thumbNotifyTimer = nil
		thumbNotifyDirty = false
		thumbNotifyMu.Unlock()
	})
	thumbNotifyMu.Unlock()
}

// emitThumbProgress 立即通知前端刷新（预生成定时器/完成时使用，不走防抖）
func (a *App) emitThumbProgress() {
	wailsruntime.EventsEmit(a.ctx, "thumb:progress", map[string]interface{}{})
}

// CleanOrphanedThumbs 清理 BoltDB 中孤立缩略图（没有对应 images 记录的 key）
func (a *App) CleanOrphanedThumbs() map[string]interface{} {
	if a.thumbDB == nil {
		return map[string]interface{}{"success": false, "error": "BoltDB 未初始化"}
	}

	a.mu.RLock()
	validIDs := make(map[string]bool, len(a.images))
	for id := range a.images {
		validIDs[id] = true
	}
	a.mu.RUnlock()

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
	a.mu.RLock()
	validIDs := make(map[string]bool, len(a.images))
	for id := range a.images {
		validIDs[id] = true
	}
	a.mu.RUnlock()

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
	a.thumbDB.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(thumbBucket)
		for _, id := range ids {
			b.Delete([]byte(id))
		}
		return nil
	})
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

	// 1. 关闭旧数据库
	if a.thumbDB != nil {
		a.thumbDB.Close()
		a.thumbDB = nil
	}
	a.mu.Lock()
	oldDB := a.imageDB
	a.imageDB = nil
	oldUserDataDB := a.userDataDB
	a.userDataDB = nil
	a.mu.Unlock()
	if oldDB != nil {
		oldDB.Close()
	}
	if oldUserDataDB != nil {
		oldUserDataDB.Close()
	}

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
		a.openThumbDB()
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

	os.MkdirAll(filepath.Dir(newThumbDBPath), 0755)
	time.Sleep(50 * time.Millisecond)
	var newThumb *bbolt.DB
	if newThumbDBPath != "" {
		newThumb, err := bbolt.Open(newThumbDBPath, 0644, &bbolt.Options{Timeout: 3 * time.Second})
		if err != nil {
			fmt.Printf("[热切换] 打开新 BoltDB 失败: %v\n", err)
		} else {
			newThumb.Update(func(tx *bbolt.Tx) error {
				_, err := tx.CreateBucketIfNotExists([]byte("thumbs"))
				return err
			})
		}
	}

	fmt.Printf("[热切换] 已加载: images=%d, folders=%d, roots=%d\n",
		len(r.images), len(r.folderIdx), len(newRegisteredRoots))

	// 4. 原子替换
	a.mu.Lock()
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
	return map[string]interface{}{"success": true, "thumbDir": path, "message": "路径已保存（重启后生效）"}
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

	// 获取 BoltDB 文件大小
	dbPath := filepath.Join(a.userDataDir, "thumbnails.db")
	if info, err := os.Stat(dbPath); err == nil {
		dbFileSize = info.Size()
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
