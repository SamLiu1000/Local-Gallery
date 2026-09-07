package main

import (
	"fmt"
	"hash/fnv"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rwcarlsen/goexif/exif"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	_ "golang.org/x/image/webp"

	"local-gallery/internal/database"
)

// ==================== 扫描功能 ====================

func (a *App) scanAllFolders() int {
	if a.bgPaused.Load() == 1 {
		fmt.Println("[扫描] 后台已暂停，跳过全量扫描")
		return 0
	}
	a.scanMu.Lock()
	if a.scanningRoots["__all__"] {
		a.scanMu.Unlock()
		fmt.Println("[扫描] 全量扫描正在进行中，跳过重复请求")
		return 0
	}
	a.scanningRoots["__all__"] = true
	a.scanMu.Unlock()
	defer func() {
		a.scanMu.Lock()
		delete(a.scanningRoots, "__all__")
		a.scanMu.Unlock()
	}()

	a.mu.Lock()
	// 检查是否需要增量扫描：已有图片数据时，只扫描缺失的根目录
	hasExistingData := len(a.images) > 0
	// ★ 排除嵌套虚拟根：其内容已由父目录扫描持有，独立扫描会把同一批文件
	//   重新挂到嵌套根下、破坏父目录计数（乙方案，浅层根持有所有权）。
	roots := make([]string, 0, len(a.registeredRoots))
	for r := range a.registeredRoots {
		if a.isRootNestedLocked(r) {
			continue
		}
		roots = append(roots, r)
	}
	a.mu.Unlock()

	// 如果已有数据，只扫描 folderCount 为空的根目录（增量模式）
	if hasExistingData {
		a.mu.RLock()
		// ★ 检查 folderIndex（而非 folderCount），folderIndex 为空才需要扫描
		folderIndexEmpty := len(a.folderIndex) == 0
		var missingRoots []string
		for _, r := range roots {
			rNorm := strings.ReplaceAll(r, "\\", "/")
			if folderIndexEmpty || len(a.folderIndex[rNorm]) == 0 {
				if cnt, ok := a.folderCount[rNorm]; !ok || cnt == 0 {
					missingRoots = append(missingRoots, r)
				}
			}
		}
		a.mu.RUnlock()
		if len(missingRoots) > 0 {
			fmt.Printf("[扫描] 增量扫描 %d 个缺失根目录（保留现有 %d 张图片）\n", len(missingRoots), len(a.images))
			// ★ 串行扫描：scanRootAsync 末尾 rebuildFolderCountsFromSQLLocked 会
			//   重建整个 folderCount，若并行扫会导致计数互相覆盖回退。
			for _, rootPath := range missingRoots {
				// ★ 跳过正在被快速导入扫描的根目录，避免并发抢改同一批索引/SQLite
				normPath, _ := filepath.Abs(rootPath)
				a.scanMu.Lock()
				beingScanned := a.scanningRoots[normPath]
				a.scanMu.Unlock()
				if beingScanned {
					fmt.Printf("[扫描] 跳过正在扫描的根目录(避免并发抢改): %s\n", normPath)
					continue
				}
				a.scanRootAsync(rootPath)
			}
			return 0 // 增量扫描是异步的，不返回总数
		}
		fmt.Printf("[扫描] 所有根目录已有数据，跳过扫描\n")
		return 0
	}

	if len(roots) == 0 {
		fmt.Printf("[扫描] 没有已注册的根目录，跳过扫描\n")
		return 0
	}

	// ★ 全量扫描：复用 scanRootAsync 的分批机制，边扫边涨。
	//   每个 root 用 scanWalkBatched 按目录分批写入内存 + 更新 folderCount +
	//   发 scan:batch 事件，前端侧栏数字随扫描实时增长；全部完成后统一发
	//   scan:complete。相比旧的"并行扫描→一次性合并"方案，用户能立即看到
	//   数字变化并点击已扫到的图片。
	//   注意：多个 root 必须串行扫描——scanRootAsync 末尾会
	//   rebuildFolderCountsFromSQLLocked 用 SQLite 重建整个 folderCount，
	//   若并行扫，先扫完的 root 会把仍在内存增长、尚未落库的 root 计数冲掉，
	//   导致侧栏数字回退。串行保证每个 root 扫完都已落库，重建计数正确。
	totalCount := 0
	scannedAny := false
	for _, rootPath := range roots {
		normalizedPath, err := filepath.Abs(rootPath)
		if err != nil {
			continue
		}
		info, err := os.Stat(normalizedPath)
		if err != nil || !info.IsDir() {
			continue
		}
		// ★ 跳过"正在被其它扫描(如快速导入 countFilesQuick)处理"的根目录：
		//   并发改同一批 memory/SQLite 会互相覆盖计数和子文件夹索引。
		a.scanMu.Lock()
		beingScanned := a.scanningRoots[normalizedPath]
		a.scanMu.Unlock()
		if beingScanned {
			fmt.Printf("[扫描] 跳过正在扫描的根目录(避免并发抢改): %s\n", normalizedPath)
			continue
		}
		totalCount += a.scanRootAsync(normalizedPath)
		scannedAny = true
	}

	// ★ 只有真正扫到了东西才重建计数/落库/失效缓存。
	//   若所有根都在被快速导入扫描(全被跳过)，直接返回——否则末尾的
	//   rebuildFolderCounts()/saveImageIndexToSQLite() 会用当前不完整的 a.images
	//   把在跑扫描的 folderCount 怼回部分值、或用部分数据覆盖 image_cache。
	if !scannedAny {
		fmt.Printf("[扫描] 没有实际扫描的根目录(均在扫描中/无效)，跳过计数重建与落库\n")
		return 0
	}

	a.mu.Lock()
	a.rebuildFolderCounts()
	a.mu.Unlock()

	a.saveImageIndexToSQLite()
	// ★ 全部数据落库后才失效预览缓存：扫描中保持旧缓存（避免 scan:batch 反复重查），
	//   完成后一次性重建，保证最终 GetFolders 用完整数据重新填充
	a.invalidatePreviewCache()
	// 数据变更：生成参数标签缓存一并失效
	a.invalidateParamTags()
	fmt.Printf("[扫描] 全部完成: 共 %d 张图片，%d 个根目录\n", totalCount, len(roots))
	// 通知前端扫描完成
	if a.ctx != nil {
		thumbCount := 0
		if thumbCounts := a.getCachedThumbCounts(); thumbCounts != nil {
			thumbCount = thumbCounts[""]
		}
		wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
			"rootPath":   "",
			"count":      totalCount,
			"thumbCount": thumbCount,
		})
	}
	return totalCount
}

// getImageDimensions 读取图片宽高（考虑 EXIF 旋转方向），只解码头部
func getImageDimensions(filePath string) (int, int) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return GetImageDimensionsVips(filePath)
	}
	w, h := cfg.Width, cfg.Height

	// 读取 EXIF Orientation 标签，如果图片需要旋转 90° 或 270°，交换宽高。
	// ★ 复用同一个文件句柄(seek 回 0 再 exif.Decode)，避免慢盘(如 L 盘)对同一文件二次打开
	//   的那一次额外磁盘读——实测慢盘上单张尺寸读取 ~920ms，二次打开要占约一半。
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		if orientation := readEXIFOrientationFromReader(f); orientation >= 5 && orientation <= 8 {
			w, h = h, w
		}
	}
	return w, h
}

// readEXIFOrientationFromReader 从已打开的 reader 读取 EXIF Orientation 标签值。
func readEXIFOrientationFromReader(r io.Reader) int {
	x, err := exif.Decode(r)
	if err != nil {
		return 0
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return 0
	}
	val, err := tag.Int(0)
	if err != nil {
		return 0
	}
	return val
}

// readEXIFOrientation 读取 JPEG 文件的 EXIF Orientation 标签值
// 返回 0 表示没有 EXIF 或没有 Orientation 标签
func readEXIFOrientation(filePath string) int {
	f, err := os.Open(filePath)
	if err != nil {
		return 0
	}
	defer f.Close()
	x, err := exif.Decode(f)
	if err != nil {
		return 0
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return 0
	}
	val, err := tag.Int(0)
	if err != nil {
		return 0
	}
	return val
}

// walkScanResult holds the accumulated scan results from a traversal.
type walkScanResult struct {
	count       int
	images      map[string]*ImageEntry
	folderIndex map[string][]string
}

// scanWalk recursively scans rootPath using os.ReadDir (which follows directory
// junctions on Windows, unlike filepath.WalkDir). Inaccessible subdirectories
// are logged and skipped without aborting the rest of the traversal.
func (a *App) scanWalk(rootPath string, externalImages map[string]*ImageEntry, externalFolderIndex map[string][]string, writeGlobal bool) walkScanResult {
	res := walkScanResult{
		count:       0,
		images:      externalImages,
		folderIndex: externalFolderIndex,
	}
	a.scanWalkDir(rootPath, rootPath, &res, writeGlobal)
	return res
}

// scanWalkDir is the recursive worker for scanWalk.
func (a *App) scanWalkDir(dirPath, rootPath string, res *walkScanResult, writeGlobal bool) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		fmt.Printf("[扫描] 跳过目录 %s: %v\n", dirPath, err)
		return
	}
	rootNorm := strings.ReplaceAll(rootPath, "\\", "/")
	for _, entry := range entries {
		if entry.IsDir() {
			a.scanWalkDir(filepath.Join(dirPath, entry.Name()), rootPath, res, writeGlobal)
			continue
		}
		if !isImageFile(entry.Name()) && !isVideoFile(entry.Name()) {
			continue
		}
		fullPath := filepath.Join(dirPath, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			fmt.Printf("[扫描] 跳过文件 %s: %v\n", fullPath, infoErr)
			continue
		}
		id := generateStableID(fullPath, info.Size(), info.ModTime().UnixMilli())
		relFolder, _ := filepath.Rel(rootPath, filepath.Dir(fullPath))
		relFolder = strings.ReplaceAll(relFolder, "\\", "/")
		if relFolder == "." {
			relFolder = ""
		}
		isVideo := isVideoFile(entry.Name())
		w, h := 0, 0
		// ★ 扫描阶段不逐张读取图片尺寸：getImageDimensions 需解码头部 + 读整个文件解析
		//   EXIF，在图片多/慢盘上会拖慢甚至卡住扫描（导致 scan:complete 迟迟不发、
		//   图廊一直不出图）。尺寸改为 GetImages 按需懒加载（SQLite 与内存回退两条
		//   取图路径均有 w/h==0 时的兜底）。
		entryObj := &ImageEntry{
			ID:           id,
			Path:         fullPath,
			Name:         entry.Name(),
			Size:         info.Size(),
			LastModified: info.ModTime().UnixMilli(),
			CreatedAt:    getFileCreationTimeMillis(fullPath, info),
			Folder:       relFolder,
			RootPath:     rootPath,
			URL:          fmt.Sprintf("/image/%s", id),
			Width:        w,
			Height:       h,
			IsVideo:      isVideo,
		}
		folderKey := rootNorm
		if relFolder != "" {
			folderKey = rootNorm + "/" + relFolder
		}
		if writeGlobal {
			a.mu.Lock()
			a.images[id] = entryObj
			a.folderIndex[folderKey] = append(a.folderIndex[folderKey], id)
			a.mu.Unlock()
		} else {
			res.images[id] = entryObj
			res.folderIndex[folderKey] = append(res.folderIndex[folderKey], id)
		}
		res.count++
	}
}

// scanWalkBatched 按目录粒度增量扫描，每个目录完成后回调一次。
func (a *App) scanWalkBatched(rootPath string, onBatch func(images map[string]*ImageEntry, folderIndex map[string][]string, folderRel string, count int)) {
	a.scanWalkDirBatched(rootPath, rootPath, "", onBatch)
}

// scanWalkDirBatched 递归遍历，每层目录扫描完后立即回调。
func (a *App) scanWalkDirBatched(dirPath, rootPath, currentRel string, onBatch func(images map[string]*ImageEntry, folderIndex map[string][]string, folderRel string, count int)) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		fmt.Printf("[扫描] 跳过目录 %s: %v\n", dirPath, err)
		return
	}
	rootNorm := strings.ReplaceAll(rootPath, "\\", "/")
	batchImages := make(map[string]*ImageEntry)
	batchFolderIndex := make(map[string][]string)

	// ★ 记录目录 mtime：扫描/导入时顺带把各目录修改时间记入缓存，
	//   使"导入后第一次刷新"也能直接跳过未变的叶子目录，不必全量重扫。
	if dInfo, err := os.Stat(dirPath); err == nil {
		a.mu.Lock()
		a.recordDirMtime(strings.ReplaceAll(dirPath, "\\", "/"), dInfo.ModTime().UnixMilli())
		a.mu.Unlock()
	}

	for _, entry := range entries {
		if entry.IsDir() {
			subRel := entry.Name()
			if currentRel != "" {
				subRel = currentRel + "/" + entry.Name()
			}
			a.scanWalkDirBatched(filepath.Join(dirPath, entry.Name()), rootPath, subRel, onBatch)
			continue
		}
		if !isImageFile(entry.Name()) && !isVideoFile(entry.Name()) {
			continue
		}
		fullPath := filepath.Join(dirPath, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			fmt.Printf("[扫描] 跳过文件 %s: %v\n", fullPath, infoErr)
			continue
		}
		id := generateStableID(fullPath, info.Size(), info.ModTime().UnixMilli())
		relFolder, _ := filepath.Rel(rootPath, filepath.Dir(fullPath))
		relFolder = strings.ReplaceAll(relFolder, "\\", "/")
		if relFolder == "." {
			relFolder = ""
		}
		isVideo := isVideoFile(entry.Name())
		w, h := 0, 0
		// ★ 扫描阶段不逐张读取图片尺寸：getImageDimensions 需解码头部 + 读整个文件解析
		//   EXIF，在图片多/慢盘上会拖慢甚至卡住扫描（导致 scan:complete 迟迟不发、
		//   图廊一直不出图）。尺寸改为 GetImages 按需懒加载（SQLite 与内存回退两条
		//   取图路径均有 w/h==0 时的兜底）。
		entryObj := &ImageEntry{
			ID:           id,
			Path:         fullPath,
			Name:         entry.Name(),
			Size:         info.Size(),
			LastModified: info.ModTime().UnixMilli(),
			CreatedAt:    getFileCreationTimeMillis(fullPath, info),
			Folder:       relFolder,
			RootPath:     rootPath,
			URL:          fmt.Sprintf("/image/%s", id),
			Width:        w,
			Height:       h,
			IsVideo:      isVideo,
		}
		folderKey := rootNorm
		if relFolder != "" {
			folderKey = rootNorm + "/" + relFolder
		}
		batchImages[id] = entryObj
		batchFolderIndex[folderKey] = append(batchFolderIndex[folderKey], id)
	}

	if len(batchImages) > 0 {
		onBatch(batchImages, batchFolderIndex, currentRel, len(batchImages))
	}
}

// getFileCreationTimeMillis 获取文件创建时间（毫秒时间戳）
// 优先从传入的 info 获取，避免重复 os.Stat
func getFileCreationTimeMillis(filePath string, existingInfo os.FileInfo) int64 {
	if existingInfo != nil {
		if stat, ok := existingInfo.Sys().(*syscall.Win32FileAttributeData); ok {
			return stat.CreationTime.Nanoseconds() / 1e6
		}
		return existingInfo.ModTime().UnixMilli()
	}
	// 兼容旧调用方式
	info, err := os.Stat(filePath)
	if err != nil {
		return 0
	}
	if stat, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return stat.CreationTime.Nanoseconds() / 1e6
	}
	return info.ModTime().UnixMilli()
}

func (a *App) removeByRoot(rootPath string) {
	a.removeByRootFromDB(rootPath)

	// 清理 image_cache 表
	if a.imageDB != nil {
		if deleted, err := a.imageDB.DeleteImageCacheByRoot(rootPath); err != nil {
			fmt.Printf("[清理] 从数据库删除缓存记录失败 [%s]: %v\n", rootPath, err)
		} else if deleted > 0 {
			fmt.Printf("[清理] 已从数据库删除 %d 条缓存记录 [%s]\n", deleted, rootPath)
		}
	}

	// 收集要删除的图片ID，用于清理缩略图（从 folderIndex 收集，不遍历 a.images）
	a.mu.RLock()
	var idsToRemove []string
	rootNorm := strings.ReplaceAll(rootPath, "\\", "/")
	for folderKey, ids := range a.folderIndex {
		if folderKey == rootNorm || strings.HasPrefix(folderKey, rootNorm+"/") {
			idsToRemove = append(idsToRemove, ids...)
		}
	}
	a.mu.RUnlock()

	a.mu.Lock()
	a.removeByRootFromMemory(rootPath)
	a.mu.Unlock()

	// 清理对应的缩略图缓存
	a.removeThumbsByIDs(idsToRemove)
}

// removeByRootFromMemory 只清除内存中的 images 和 folderIndex
// removeByRootFromMemory 从内存清理某 rootPath 的所有数据（需持 a.mu 写锁）。
// 基于 folderIndex 定位 root 下所有 folderKey，同步清理 LRU 状态。
func (a *App) removeByRootFromMemory(rootPath string) {
	rootNorm := strings.ReplaceAll(rootPath, "\\", "/")
	// 收集 root 下所有 folderKey
	var keysToRemove []string
	for folderKey := range a.folderIndex {
		if folderKey == rootNorm || strings.HasPrefix(folderKey, rootNorm+"/") {
			keysToRemove = append(keysToRemove, folderKey)
		}
	}
	// 从 a.images 删除这些 folderKey 下的所有 ID，并清 LRU
	for _, fk := range keysToRemove {
		for _, id := range a.folderIndex[fk] {
			delete(a.images, id)
		}
		delete(a.folderIndex, fk)
		delete(a.folderLoaded, fk)
		if elem, ok := a.lruNodes[fk]; ok {
			a.folderLRU.Remove(elem)
			delete(a.lruNodes, fk)
		}
	}
	// 清理 folderCount
	delete(a.folderCount, rootNorm)
	for k := range a.folderCount {
		if strings.HasPrefix(k, rootNorm+"/") {
			delete(a.folderCount, k)
		}
	}
}

// removeByRootFromDB 只清除 SQLite 中对应 root_path 的记录
func (a *App) removeByRootFromDB(rootPath string) {
	if a.imageDB != nil {
		deleted, err := a.imageDB.DeleteByRoot(rootPath)
		if err != nil {
			fmt.Printf("[清理] 从数据库删除根目录记录失败 [%s]: %v\n", rootPath, err)
		} else if deleted > 0 {
			fmt.Printf("[清理] 已从数据库删除 %d 条根目录记录 [%s]\n", deleted, rootPath)
		}
	}
}

func (a *App) rebuildFolderCounts() {
	counts := make(map[string]int)
	for _, entry := range a.images {
		rootPath := strings.ReplaceAll(entry.RootPath, "\\", "/")
		folder := strings.ReplaceAll(entry.Folder, "\\", "/")
		counts[rootPath]++
		if folder != "" {
			parts := strings.Split(folder, "/")
			for i := 1; i <= len(parts); i++ {
				subPath := rootPath + "/" + strings.Join(parts[:i], "/")
				counts[subPath]++
			}
		}
	}
	a.folderCount = counts
}

// incrementFolderCounts incrementally updates folderCount for new entries only.
func (a *App) incrementFolderCounts(entries map[string]*ImageEntry) {
	if a.folderCount == nil {
		a.folderCount = make(map[string]int)
	}
	for _, entry := range entries {
		rp := strings.ReplaceAll(entry.RootPath, "\\", "/")
		fd := strings.ReplaceAll(entry.Folder, "\\", "/")
		a.folderCount[rp]++
		if fd != "" {
			parts := strings.Split(fd, "/")
			for i := 1; i <= len(parts); i++ {
				a.folderCount[rp+"/"+strings.Join(parts[:i], "/")]++
			}
		}
	}
}

// mergeScanBatchIntoMemory 把一批扫描结果"幂等"合并进内存。
// ★ 幂等：已在 a.images 中的 ID 直接跳过——这样即席扫描（GetImages 点开子文件夹）
//   与后台批量扫描（countFilesQuick/scanRootAsync）可能扫到同一批文件时，
//   folderIndex 不会重复追加、folderCount 不会重复计数。
//   folderIndex 只追加本次"真正新增"的 ID；folderCount 只对新增图片计数。
// 自管 a.mu 锁。返回本次新增的 entries（供调用方统计/回传）。
func (a *App) mergeScanBatchIntoMemory(batchImages map[string]*ImageEntry, batchFolderIndex map[string][]string) map[string]*ImageEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	newEntries := make(map[string]*ImageEntry, len(batchImages))
	for k, v := range batchImages {
		if _, exists := a.images[k]; exists {
			continue
		}
		a.images[k] = v
		newEntries[k] = v
	}
	if len(newEntries) == 0 {
		return newEntries
	}
	for fk, ids := range batchFolderIndex {
		for _, id := range ids {
			if _, ok := newEntries[id]; ok {
				a.folderIndex[fk] = append(a.folderIndex[fk], id)
			}
		}
	}
	a.incrementFolderCounts(newEntries)
	return newEntries
}

// folderEntriesFromMemory 收集某文件夹子树下（含自身）已索引到内存的全部图片。
func (a *App) folderEntriesFromMemory(folder string) []*ImageEntry {
	normalized := strings.ReplaceAll(folder, "\\", "/")
	a.mu.RLock()
	defer a.mu.RUnlock()
	seen := make(map[string]bool)
	var results []*ImageEntry
	for fk, ids := range a.folderIndex {
		if fk == normalized || strings.HasPrefix(fk, normalized+"/") {
			for _, id := range ids {
				if seen[id] {
					continue
				}
				seen[id] = true
				if e, ok := a.images[id]; ok {
					results = append(results, e)
				}
			}
		}
	}
	return results
}

// rebuildFolderIndexForRoot 导入完成后用 a.images 权威重建"某个根目录"的 folderIndex/folderCount。
// ★ 目的：countFilesQuick 扫描过程中 folderIndex 是增量写入的，若期间被并发扫描/即席扫描
//   抢写过，或幂等合并跳过了一些，导入结束时 folderIndex 可能只覆盖部分子文件夹，
//   导致侧栏"子文件夹只显示部分，刷新页面才全"。此处用内存里该根目录的完整图片集合重算，
//   保证 GetFolders→buildFolderTreeFromIndex 能列出全部非空子文件夹。
// 只影响 rootPath 这棵子树，不碰其它根。
func (a *App) rebuildFolderIndexForRoot(rootPath string) {
	rootNorm := strings.ReplaceAll(rootPath, "\\", "/")
	a.mu.Lock()
	defer a.mu.Unlock()
	// 1. 清掉本根目录下现有的 folderIndex 键与计数，避免残留（可能来自被中途替换的部分扫描）
	for fk := range a.folderIndex {
		if fk == rootNorm || strings.HasPrefix(fk, rootNorm+"/") {
			delete(a.folderIndex, fk)
		}
	}
	for k := range a.folderCount {
		if k == rootNorm || strings.HasPrefix(k, rootNorm+"/") {
			delete(a.folderCount, k)
		}
	}
	// 2. 从 a.images 重建（仅本根目录；扫描刚结束，该根全部图片都在 a.images 中，未被 LRU 淘汰）
	folderIdx := make(map[string][]string)
	counts := make(map[string]int)
	for id, e := range a.images {
		rp := strings.ReplaceAll(e.RootPath, "\\", "/")
		if rp != rootNorm {
			continue
		}
		fk := rootNorm
		var fd string
		if e.Folder != "" {
			fd = strings.ReplaceAll(e.Folder, "\\", "/")
			fk = rootNorm + "/" + fd
		}
		folderIdx[fk] = append(folderIdx[fk], id)
		counts[rootNorm]++
		if fd != "" {
			parts := strings.Split(fd, "/")
			for i := 1; i <= len(parts); i++ {
				counts[rootNorm+"/"+strings.Join(parts[:i], "/")]++
			}
		}
	}
	for k, v := range folderIdx {
		a.folderIndex[k] = append(a.folderIndex[k], v...)
		// ★ 标记已加载：这些图片都已写入 a.images，避免之后 ensureFolderLoaded 再从 DB 追加重复 ID
		if !a.folderLoaded[k] {
			a.folderLoaded[k] = true
			if a.folderLRU != nil {
				a.lruNodes[k] = a.folderLRU.PushBack(k)
			}
		}
	}
	for k, v := range counts {
		a.folderCount[k] = v
	}
}

// folderIsSubfolder 判断 normalizedFolder(正斜杠) 是否为某个已注册根目录内的"严格"子文件夹。
// GetImages 冷启动用：点开子文件夹时让即席扫描接管，而不是一刀切触发全量后台扫描。
func (a *App) folderIsSubfolder(normalized string) bool {
	lower := strings.ToLower(normalized)
	a.mu.RLock()
	defer a.mu.RUnlock()
	for root := range a.registeredRoots {
		rn := strings.ReplaceAll(root, "\\", "/")
		if strings.HasPrefix(lower, strings.ToLower(rn)+"/") {
			return true
		}
	}
	return false
}

// scanSubfolderImmediate 点开子文件夹时"即席优先扫描"：只扫这一个子文件夹（含其子树），
// 立即把结果写进内存并返回，让用户"点哪个先出哪个"，不用等整个后台大扫描扫到它。
// 以最长的已注册父根为 folderKey/RootPath 基准，保证与 GetImages/GetFolderCount 的前缀匹配一致。
// 幂等（mergeScanBatchIntoMemory），并与后台扫描并发去重（subfolderScanning）。
func (a *App) scanSubfolderImmediate(subfolder string) []*ImageEntry {
	normalized := strings.ReplaceAll(subfolder, "\\", "/")
	// 找最长匹配的已注册父根（严格子文件夹：前缀 parentRoot+"/"；大小写不敏感，避免路径大小写差异匹配不到）
	a.mu.RLock()
	var parentRoot, parentRootNorm string
	for root := range a.registeredRoots {
		rn := strings.ReplaceAll(root, "\\", "/")
		if strings.HasPrefix(strings.ToLower(normalized), strings.ToLower(rn)+"/") && len(rn) > len(parentRootNorm) {
			parentRootNorm = rn
			parentRoot = root
		}
	}
	a.mu.RUnlock()
	if parentRoot == "" {
		return nil // 不位于任何已注册根内，或本身就是根，交给常规扫描
	}
	// ★ 按长度取相对路径，避免大小写不同导致 TrimPrefix 失败
	relFolder := normalized[len(parentRootNorm)+1:]
	if relFolder == "" {
		return nil
	}

	// 并发去重：同一子文件夹只允许一个即席扫描在跑
	a.scanMu.Lock()
	if a.subfolderScanning == nil {
		a.subfolderScanning = make(map[string]bool)
	}
	if a.subfolderScanning[normalized] {
		a.scanMu.Unlock()
		return a.folderEntriesFromMemory(normalized)
	}
	a.subfolderScanning[normalized] = true
	a.scanMu.Unlock()
	defer func() {
		a.scanMu.Lock()
		delete(a.subfolderScanning, normalized)
		a.scanMu.Unlock()
	}()

	fmt.Printf("[即席扫描] 点开子文件夹，立即扫描: %s (父根: %s)\n", normalized, parentRoot)
	a.scanWalkDirBatched(subfolder, parentRoot, relFolder,
		func(batchImages map[string]*ImageEntry, batchFolderIndex map[string][]string, folderRel string, batchCount int) {
			a.mergeScanBatchIntoMemory(batchImages, batchFolderIndex)
			// 标记已加载，避免之后 ensureFolderLoaded 去 DB（可能仍为空）把内存数据当作"未加载"
			a.mu.Lock()
			for fk := range batchFolderIndex {
				if !a.folderLoaded[fk] {
					a.folderLoaded[fk] = true
					if a.folderLRU != nil {
						a.lruNodes[fk] = a.folderLRU.PushBack(fk)
					}
				}
			}
			a.mu.Unlock()
		})

	entries := a.folderEntriesFromMemory(normalized)
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "folder:count", map[string]interface{}{
			"folderPath": normalized,
			"count":      len(entries),
		})
	}
	return entries
}

func (a *App) rebuildFolderIndex() {
	idx := make(map[string][]string)
	for id, entry := range a.images {
		rootNorm := strings.ReplaceAll(entry.RootPath, "\\", "/")
		folderKey := rootNorm
		if entry.Folder != "" {
			folderKey = rootNorm + "/" + strings.ReplaceAll(entry.Folder, "\\", "/")
		}
		idx[folderKey] = append(idx[folderKey], id)
	}
	a.folderIndex = idx
}

// pruneDeadFolders 清理磁盘上已删除、但索引/数据库仍残留的幽灵文件夹。
// 根源：启动时不做全量增量刷新，文件夹被删除后其 image_cache 记录与 folderIndex key
// 会一直残留 → 导航栏出现同名重复项、图廊/预览缩略图指向不存在的文件而无法显示。
// 做法：对每个已注册根目录下的 folder key 做目录存在性检查（IO 阶段不持锁），
// 目录已不存在的 key 连同其图片记录从内存、image_cache、缩略图库中一并移除。
// 返回清理的文件夹数与图片数。
func (a *App) pruneDeadFolders() (prunedFolders int, prunedImages int) {
	a.mu.RLock()
	roots := make([]string, 0, len(a.registeredRoots))
	for r := range a.registeredRoots {
		roots = append(roots, r)
	}
	a.mu.RUnlock()
	if len(roots) == 0 {
		return 0, 0
	}

	// ★ 启动速度优化：只对"根目录 mtime 变了"的根做幽灵清理。健康启动（磁盘上无增删）
	//   直接跳过，避免每次开机对每个子文件夹目录 os.Stat——注释里"慢盘数千次约 2-3 秒"。
	//   删除顶级子文件夹/在根下增删文件都会改变根目录的 mtime，故根 mtime 不变≈无变化。
	var changedRoots []string
	for _, root := range roots {
		rootNorm := strings.ReplaceAll(root, "\\", "/")
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			// 根本身已不存在：归入死根，交由 removeByRoot 类清理
			changedRoots = append(changedRoots, root)
			continue
		}
		a.mu.RLock()
		unchanged := a.dirMtimeUnchanged(rootNorm, info.ModTime().UnixMilli())
		a.mu.RUnlock()
		if !unchanged {
			changedRoots = append(changedRoots, root)
		}
	}
	if len(changedRoots) == 0 {
		return 0, 0 // 无根变化：跳过整棵树的 stat
	}

	// 1. 收集目录已不存在的 folder key（IO 阶段不持锁）——仅遍历"变化过的根"之下
	var deadKeys []string
	for _, root := range changedRoots {
		rootNorm := strings.ReplaceAll(root, "\\", "/")
		prefix := rootNorm + "/"
		a.mu.RLock()
		var keys []string
		for k := range a.folderIndex {
			if k != rootNorm && strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		a.mu.RUnlock()
		if len(keys) == 0 {
			continue
		}
		for _, k := range keys {
			rel := strings.TrimPrefix(k, prefix)
			diskDir := filepath.Join(root, filepath.FromSlash(rel))
			info, err := os.Stat(diskDir)
			if err != nil || !info.IsDir() {
				deadKeys = append(deadKeys, k)
			}
		}
	}
	if len(deadKeys) == 0 {
		return 0, 0
	}
	return a.pruneFolderKeys(deadKeys)
}

// pruneFolderKeys 将一组已确认不存在的文件夹 key 连同其图片记录，从内存（a.images /
// folderIndex / folderCount）、image_cache、搜索索引 images 表、缩略图库中一并移除，
// 并从 SQL 重建计数；随后触发前端文件夹树刷新。
// 供"启动幽灵清理""打开已删除文件夹"等场景复用（同一套清理逻辑，防止同类问题回归）。
func (a *App) pruneFolderKeys(deadKeys []string) (prunedFolders int, prunedImages int) {
	// 1. 收集死文件夹下的全部图片 ID（内存 folderIndex + SQLite 兜底：LRU 可能已逐出内存）
	deadIDs := make(map[string]bool)
	a.mu.RLock()
	for _, k := range deadKeys {
		for _, id := range a.folderIndex[k] {
			deadIDs[id] = true
		}
	}
	a.mu.RUnlock()
	if a.imageDB != nil {
		for _, k := range deadKeys {
			if ids, err := a.imageDB.LoadImageCacheIDsUnderPath(k); err == nil {
				for _, id := range ids {
					deadIDs[id] = true
				}
			}
		}
	}

	// 2. 持久化清理：image_cache（全集）+ images（搜索索引子集）+ 缩略图
	if len(deadIDs) > 0 {
		ids := make([]string, 0, len(deadIDs))
		for id := range deadIDs {
			ids = append(ids, id)
		}
		if a.imageDB != nil {
			if err := a.imageDB.DeleteImageCacheBatch(ids); err != nil {
				fmt.Printf("[幽灵清理] 删除 image_cache 失败 (%d 条): %v\n", len(ids), err)
			}
			if err := a.imageDB.DeleteImagesBatch(ids); err != nil {
				fmt.Printf("[幽灵清理] 删除搜索索引记录失败 (%d 条): %v\n", len(ids), err)
			}
		}
		a.removeThumbsByIDs(ids)
	}

	// 3. 内存清理 + 从 SQL 重建计数（此时 DB 已无死记录）
	a.mu.Lock()
	for _, k := range deadKeys {
		delete(a.folderIndex, k)
		delete(a.folderCount, k)
	}
	for id := range deadIDs {
		delete(a.images, id)
	}
	a.evictLRU()
	a.rebuildFolderCountsFromSQLLocked()
	a.mu.Unlock()

	prunedFolders = len(deadKeys)
	prunedImages = len(deadIDs)
	fmt.Printf("[幽灵清理] 移除 %d 个已不存在的文件夹、%d 张失效图片\n", prunedFolders, prunedImages)
	a.saveImageIndex()

	// 4. 通知前端刷新文件夹树（scan:complete 会触发 Sidebar.refreshFolderTree）
	if a.ctx != nil {
		rootSet := make(map[string]bool)
		a.mu.RLock()
		for _, k := range deadKeys {
			for r := range a.registeredRoots {
				rn := strings.ReplaceAll(r, "\\", "/")
				if k == rn || strings.HasPrefix(k, rn+"/") {
					rootSet[rn] = true
				}
			}
		}
		a.mu.RUnlock()
		for rn := range rootSet {
			wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
				"rootPath":   rn,
				"count":      0,
				"added":      0,
				"removed":    prunedImages,
				"thumbCount": 0,
			})
		}
	}
	return prunedFolders, prunedImages
}

// purgeMissingImage 单张图片自愈：源文件已删除时移除其全部残留（内存索引、DB、缩略图）。
// 由缩略图生成失败路径调用——文件夹仍在但其中文件被删的"文件级幽灵"，
// 会在被浏览到的那一刻自动清除，图廊不再残留破图。
func (a *App) purgeMissingImage(id string) {
	a.mu.RLock()
	entry := a.images[id]
	a.mu.RUnlock()

	if a.imageDB != nil {
		a.imageDB.DeleteImage(id)
		a.imageDB.DeleteImageCacheBatch([]string{id})
	}
	a.removeThumbsByIDs([]string{id})

	a.mu.Lock()
	delete(a.images, id)
	if entry != nil {
		rootNorm := strings.ReplaceAll(entry.RootPath, "\\", "/")
		if a.folderCount[rootNorm] > 0 {
			a.folderCount[rootNorm]--
		}
		// 定位该图片所属的 folder key 并移除其 ID
		removeFromFolderKey := func(key string) {
			if ids, ok := a.folderIndex[key]; ok {
				var rem []string
				for _, x := range ids {
					if x != id {
						rem = append(rem, x)
					}
				}
				if len(rem) == 0 {
					delete(a.folderIndex, key)
				} else {
					a.folderIndex[key] = rem
				}
			}
		}
		if entry.Folder != "" {
			parts := strings.Split(strings.ReplaceAll(entry.Folder, "\\", "/"), "/")
			for i := 1; i <= len(parts); i++ {
				sub := rootNorm + "/" + strings.Join(parts[:i], "/")
				if a.folderCount[sub] > 0 {
					a.folderCount[sub]--
				}
				if i == len(parts) {
					removeFromFolderKey(sub)
				}
			}
		} else {
			removeFromFolderKey(rootNorm)
		}
	}
	a.mu.Unlock()
	fmt.Printf("[幽灵清理] 单张图片自愈移除: %s\n", id)
}

// buildFolderTreeFromIndex 从 folderIndex 内存构建文件夹树，零磁盘 I/O
// 比 buildFolderTreeRecursive（os.ReadDir）快几个数量级
// previews：已开启"子文件夹缩略图预览"的文件夹 key → 预览条目（由 GetFolders 预取，内存优先/SQLite 兜底）
// isRootNested 判断 rootPath 是否位于另一个已注册根目录之内（嵌套虚拟根）。
func (a *App) isRootNested(rootPath string) bool {
	norm := strings.ToLower(strings.ReplaceAll(rootPath, "\\", "/"))
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.isRootNestedLockedNorm(norm)
}

// isRootNestedLocked 判断 rootPath 是否嵌套（调用方需已持有 a.mu 读锁或写锁）。
func (a *App) isRootNestedLocked(rootPath string) bool {
	norm := strings.ToLower(strings.ReplaceAll(rootPath, "\\", "/"))
	return a.isRootNestedLockedNorm(norm)
}

// isRootNestedLockedNorm 按已规范化（小写 + 正斜杠）的路径判断是否嵌套。
func (a *App) isRootNestedLockedNorm(norm string) bool {
	for root := range a.registeredRoots {
		rn := strings.ToLower(strings.ReplaceAll(root, "\\", "/"))
		if rn != norm && strings.HasPrefix(norm, rn+"/") {
			return true
		}
	}
	return false
}

// rootSubtreeCount 返回 rootPath 目录下已索引的图片总数（含所有子目录）。
// 嵌套根的图片由父目录扫描按完整路径索引，其 folderCount 键即真实目录路径，
// 因此对前缀求和即可得到该根下全部图片数。
func (a *App) rootSubtreeCount(rootPath string) int {
	norm := strings.ReplaceAll(rootPath, "\\", "/")
	prefix := norm + "/"
	a.mu.RLock()
	defer a.mu.RUnlock()
	total := a.folderCount[norm]
	for k, c := range a.folderCount {
		if strings.HasPrefix(k, prefix) {
			total += c
		}
	}
	return total
}

func (a *App) buildFolderTreeFromIndex(rootPath string, thumbCounts map[string]int, previews map[string][]FolderPreview) []*FolderNode {
	normalizedRoot := strings.ReplaceAll(rootPath, "\\", "/")
	prefix := normalizedRoot + "/"

	// 收集该根目录下所有有图片的文件夹路径
	folderSet := make(map[string]bool)
	for folderKey := range a.folderIndex {
		if folderKey == normalizedRoot || strings.HasPrefix(folderKey, prefix) {
			folderSet[folderKey] = true
		}
	}

	// 即使没有缓存也返回空切片，让前端能显示根节点
	// 子文件夹会在后台扫描完成后通过 scan:batch 事件更新
	if len(folderSet) == 0 {
		return []*FolderNode{}
	}

	// ★ 乙方案：本根之内已注册的嵌套根（子目录被单独导入）在父目录树里渲染为
	//   "折叠入口"——不再展开其子文件夹（避免与顶层独立入口重复显示一整棵子树），
	//   其计数仍显示该嵌套根的全部图片数（folderCount 键与真实目录一致）。
	// ★ 注意：调用方(GetFolders)已持有 a.mu.RLock；此处**不能再取一次 RLock**。
	//   否则当有写者(快速导入每批的 a.mu.Lock)在排队时，Go RWMutex 会阻塞新的读锁，
	//   → GetFolders 卡在这层读锁、外层读锁不释放 → 写者也等不到锁 → 死锁，
	//   表现为"点击任何文件夹都不出图、磁盘不读、连累其它文件夹"。
	//   这里的读取依赖调用方已持有的读锁。
	var registeredUnder []string
	for root := range a.registeredRoots {
		rn := strings.ReplaceAll(root, "\\", "/")
		if rn != normalizedRoot && strings.HasPrefix(rn, prefix) {
			registeredUnder = append(registeredUnder, rn)
		}
	}
	registeredSet := make(map[string]bool, len(registeredUnder))
	for _, rn := range registeredUnder {
		registeredSet[rn] = true
	}

	// 用 map 构建树节点，key 为规范化路径
	nodes := make(map[string]*FolderNode)
	for folderPath := range folderSet {
		// 跳过根路径本身，根是 GetFolders 创建的
		if folderPath == normalizedRoot {
			continue
		}
		rel := folderPath[len(normalizedRoot)+1:]
		parts := strings.Split(rel, "/")

		// 为路径上的每一级创建节点
		for i := 0; i < len(parts); i++ {
			subPath := normalizedRoot + "/" + strings.Join(parts[:i+1], "/")
			if _, exists := nodes[subPath]; exists {
				continue
			}
			node := &FolderNode{
				Name:       parts[i],
				Path:       filepath.Join(rootPath, filepath.Join(parts[:i+1]...)),
				ImageCount: a.folderCount[subPath],
				ThumbCount: thumbCounts[subPath],
				Children:   nil,
			}
			// ★ 子文件夹缩略图预览：命中已开启预览的子树时，挂上预取的预览条目（≤4 张直接图片）
			if ps, ok := previews[subPath]; ok && len(ps) > 0 {
				node.Previews = ps
			}
			nodes[subPath] = node
		}
	}

	// 建立父子关系
	for childPath, childNode := range nodes {
		idx := strings.LastIndex(childPath, "/")
		if idx < 0 {
			continue
		}
		parentPath := childPath[:idx]
		if parentPath == normalizedRoot {
			continue // 根节点的子节点，直接由 GetFolders 挂载
		}
		if registeredSet[parentPath] {
			continue // 父级是已注册的嵌套根（折叠入口），不展开其子节点
		}
		if parent, ok := nodes[parentPath]; ok {
			parent.Children = append(parent.Children, childNode)
		}
	}

	// 只返回直接子节点（父路径为根的节点）
	directRoots := make([]*FolderNode, 0)
	for childPath, childNode := range nodes {
		idx := strings.LastIndex(childPath, "/")
		if idx >= 0 && childPath[:idx] == normalizedRoot {
			directRoots = append(directRoots, childNode)
		}
	}

	// ★ 递归聚合：中间节点的 ImageCount 由其所有子节点的 ImageCount 累加
	var aggregateCounts func(node *FolderNode) int
	aggregateCounts = func(node *FolderNode) int {
		sum := 0
		for _, c := range node.Children {
			sum += aggregateCounts(c)
		}
		if sum == 0 {
			sum = node.ImageCount // 叶子节点使用自己的 count
		}
		node.ImageCount = sum
		return sum
	}
	for _, root := range directRoots {
		aggregateCounts(root)
	}

	// 递归排序
	var sortTree func([]*FolderNode)
	sortTree = func(children []*FolderNode) {
		sort.Slice(children, func(i, j int) bool {
			return children[i].Name < children[j].Name
		})
		for _, c := range children {
			if len(c.Children) > 0 {
				sortTree(c.Children)
			}
		}
	}
	sortTree(directRoots)

	return directRoots
}

// inPreviewSubtree 判断 key（归一化路径）是否命中某个已开启预览的文件夹（自身或其祖先）
func inPreviewSubtree(key string, previewRoots map[string]bool) bool {
	if len(previewRoots) == 0 {
		return false
	}
	if previewRoots[key] {
		return true
	}
	for root := range previewRoots {
		if strings.HasPrefix(key, root+"/") {
			return true
		}
	}
	return false
}

// previewOffsets 计算确定性选取的 n 个下标（FNV 哈希 key 做种子，同一 key 结果稳定，不同 key 各不相同）。
// 内存路径（切片）与 SQLite 兜底路径（COUNT+OFFSET）共用同一算法，保证两种来源的选取一致风格。
func previewOffsets(key string, total, n int) []int {
	if total <= 0 || n <= 0 {
		return nil
	}
	if total <= n {
		offs := make([]int, total)
		for i := range offs {
			offs[i] = i
		}
		return offs
	}
	h := fnv.New32a()
	h.Write([]byte(key))
	seed := h.Sum32()
	step := uint32(total) / uint32(n)
	if step == 0 {
		step = 1
	}
	offs := make([]int, 0, n)
	for i := 0; i < n; i++ {
		offs = append(offs, int((seed+uint32(i)*step)%uint32(total)))
	}
	return offs
}

// pickFolderPreviewIDs 从文件夹直接图片 ID 中确定性取 ≤maxN 个（内存 folderIndex 路径）。
func pickFolderPreviewIDs(ids []string, key string, maxN int) []string {
	if len(ids) == 0 || maxN <= 0 {
		return nil
	}
	offs := previewOffsets(key, len(ids), maxN)
	if offs == nil {
		return nil
	}
	out := make([]string, 0, len(offs))
	for _, o := range offs {
		out = append(out, ids[o])
	}
	return out
}

// pickFolderPreviews 取 ≤maxN 条预览（含 lastModified 与 path），供前端复用图廊缩略图 URL 构造、点击定位图片。
// a.images 中未命中的条目（如 LRU 驱逐后）回退 lastModified=0/path 为空，不影响缩略图加载。
func (a *App) pickFolderPreviews(ids []string, key string, maxN int) []FolderPreview {
	sel := pickFolderPreviewIDs(ids, key, maxN)
	if len(sel) == 0 {
		return nil
	}
	out := make([]FolderPreview, 0, len(sel))
	for _, id := range sel {
		lm := int64(0)
		p := ""
		if e := a.images[id]; e != nil {
			lm = e.LastModified
			p = e.Path
		}
		out = append(out, FolderPreview{ID: id, LastModified: lm, Path: p})
	}
	return out
}

func (a *App) buildFolderTreeRecursive(currentPath, rootPath string) []*FolderNode {
	var children []*FolderNode
	entries, err := os.ReadDir(currentPath)
	if err != nil {
		return children
	}
	for _, entry := range entries {
		if entry.IsDir() {
			fullPath := filepath.Join(currentPath, entry.Name())
			subChildren := a.buildFolderTreeRecursive(fullPath, rootPath)
			normalizedPath := strings.ReplaceAll(fullPath, "\\", "/")
			imageCount := a.folderCount[normalizedPath]
			if imageCount > 0 || len(subChildren) > 0 {
				children = append(children, &FolderNode{
					Name:       entry.Name(),
					Path:       fullPath,
					ImageCount: imageCount,
					Children:   subChildren,
				})
			}
		}
	}
	sort.Slice(children, func(i, j int) bool {
		return children[i].Name < children[j].Name
	})
	return children
}

func (a *App) scanRootAsync(rootPath string) int {
	if a.bgPaused.Load() == 1 {
		return 0
	}
	a.scanMu.Lock()
	if a.scanningRoots[rootPath] {
		a.scanMu.Unlock()
		return 0
	}
	a.scanningRoots[rootPath] = true
	a.scanMu.Unlock()
	defer func() { a.scanMu.Lock(); delete(a.scanningRoots, rootPath); a.scanMu.Unlock() }()
	// ★ 持久化"扫描中"标记：扫描中途退出时残留，下次启动 ensureImageIndex 补扫
	a.setScanInProgress(rootPath, true)
	fmt.Printf("[后台扫描] 开始扫描: %s\n", rootPath)

	// 通知前端开始扫描
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "scan:start", map[string]interface{}{
			"rootPath": rootPath,
		})
	}

	a.removeByRootFromDB(rootPath)

	localImages := make(map[string]*ImageEntry)
	localFolderIndex := make(map[string][]string)
	totalCount := 0

	a.scanWalkBatched(rootPath, func(batchImages map[string]*ImageEntry, batchFolderIndex map[string][]string, folderRel string, batchCount int) {
		a.mu.Lock()
		if !a.registeredRoots[rootPath] {
			a.mu.Unlock()
			return
		}
		for k, v := range batchImages {
			a.images[k] = v
			localImages[k] = v
		}
		for k, v := range batchFolderIndex {
			a.folderIndex[k] = append(a.folderIndex[k], v...)
			localFolderIndex[k] = append(localFolderIndex[k], v...)
		}
		a.incrementFolderCounts(batchImages)
		a.mu.Unlock()

		totalCount += batchCount
		if a.ctx != nil && batchCount > 0 {
			thumbCount := 0
			if thumbCounts := a.getCachedThumbCounts(); thumbCounts != nil {
				thumbCount = thumbCounts[strings.ReplaceAll(rootPath, "\\", "/")]
			}
			wailsruntime.EventsEmit(a.ctx, "scan:batch", map[string]interface{}{
				"rootPath":   rootPath,
				"folder":     folderRel,
				"count":      batchCount,
				"totalSoFar": totalCount,
				"thumbCount": thumbCount,
			})
		}
	})

	a.mu.Lock()
	if !a.registeredRoots[rootPath] {
		a.mu.Unlock()
		fmt.Printf("[后台扫描] 根目录已被移除，丢弃扫描结果: %s\n", rootPath)
		return 0
	}
	a.removeByRootFromMemory(rootPath)
	for k, v := range localImages {
		a.images[k] = v
	}
	for k, v := range localFolderIndex {
		a.folderIndex[k] = append(a.folderIndex[k], v...)
		if !a.folderLoaded[k] {
			a.folderLoaded[k] = true
			if a.folderLRU != nil {
				a.lruNodes[k] = a.folderLRU.PushBack(k)
			}
		} else {
			a.touchFolderLocked(k)
		}
	}
	a.mu.Unlock()

	// ★ 先完整持久化到 SQLite，再执行 LRU 逐出。
	//   若先 evictLRU，maxLoadedFolders(32) 会把本 root 下 92 个子文件夹
	//   中较旧的图片从 a.images 删除，saveImageIndexByRoot 遍历 folderIndex
	//   时找不到对应图片，导致 SQLite 只写入部分数据（如 6107/16364），
	//   侧栏计数与 GetImages total 也随之偏小。
	a.saveImageIndexForRoot(rootPath)
	// ★ 扫描完成，清除"扫描中"标记
	a.setScanInProgress(rootPath, false)

	a.mu.Lock()
	a.evictLRU()
	a.rebuildFolderCountsFromSQLLocked()
	a.mu.Unlock()

	// ★ 扫描完成：自动低优预生成缺失缩略图（后台 goroutine，不阻塞 scan:complete）。
	//   新导入的文件夹在用户滚动前就补齐缩略图，避免首屏滚动卡顿。
	if totalCount > 0 && len(localImages) > 0 {
		entries := make([]*ImageEntry, 0, len(localImages))
		for _, e := range localImages {
			entries = append(entries, e)
		}
		go a.triggerAutoPreGen(filepath.Base(rootPath), entries)
	}

	// 再次确保 folderCount 正确后再通知完成
	a.mu.Lock()
	folderCount := a.folderCount[strings.ReplaceAll(rootPath, "\\", "/")]
	a.mu.Unlock()
	fmt.Printf("[后台扫描] 验证: rootPath=%s, folderCount=%d, folderIndexKeys=%d\n", rootPath, folderCount, len(localFolderIndex))

	ft := a.folderTypes[rootPath]
	if ft == "" {
		ft = "ai"
	}
	if ft != "photo" {
		go a.batchIndexImages(localImages, ft)
	}
	fmt.Printf("[后台扫描] 完成: %s，共 %d ���图片\n", rootPath, totalCount)
	if a.ctx != nil {
		thumbCount := 0
		if thumbCounts := a.getCachedThumbCounts(); thumbCounts != nil {
			thumbCount = thumbCounts[strings.ReplaceAll(rootPath, "\\", "/")]
		}
		wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
			"rootPath":   rootPath,
			"count":      totalCount,
			"thumbCount": thumbCount,
		})
	}
	return totalCount
}
func (a *App) ensureImageIndex() {
	a.mu.RLock()
	totalFolders := len(a.folderIndex)
	totalRoots := len(a.registeredRoots)
	a.mu.RUnlock()

	// 快速路径：没有已注册根目录，无需检查
	if totalRoots == 0 {
		return
	}

	// 快速路径：folderIndex 有记录且所有根目录在 folderCount 中都有记录
	if totalFolders > 0 {
		a.mu.RLock()
		allOk := true
		for root := range a.registeredRoots {
			rootNorm := strings.ReplaceAll(root, "\\", "/")
			if count, ok := a.folderCount[rootNorm]; !ok || count == 0 {
				// 检查是否有子文件夹的图片
				hasSubImages := false
				for key, c := range a.folderCount {
					if c > 0 && strings.HasPrefix(key, rootNorm+"/") {
						hasSubImages = true
						break
					}
				}
				if !hasSubImages {
					allOk = false
					break
				}
			}
		}
		a.mu.RUnlock()
		if allOk {
			// ★ 修复：快速路径通过后再检查是否有上次中断的扫描（标记残留）。
			//   中断的扫描（导入/重扫中途退出）会让 image_cache 停在半成品，
			//   计数对不上总数且手动刷新不补扫。发现残留标记 → 不能跳过，需补扫。
			interrupted := a.getInterruptedScans()
			if len(interrupted) == 0 {
				fmt.Printf("[启动修复] folderIndex 完整（%d 个文件夹，%d 个根目录），跳过检查\n", totalFolders, totalRoots)
				return
			}
			fmt.Printf("[启动修复] 发现 %d 个上次中断的扫描，进入补扫流程\n", len(interrupted))
		}
	}

	// 详细检查：找出缺失索引的根目录并补扫
	// ★ 同时清理已不存在的孤立路径（先复制路径列表，释放锁后再 IO 检查）
	a.mu.RLock()
	rootsCopy := make([]string, 0, len(a.registeredRoots))
	for root := range a.registeredRoots {
		rootsCopy = append(rootsCopy, root)
	}
	a.mu.RUnlock()

	var rootsToScan []string
	var deadRoots []string

	for _, root := range rootsCopy {
		// ★ 修复：对瞬时 os.Stat 失败做一次重试（慢盘/网络盘挂载延迟时可能误判"目录不存在"，
		//   导致已注册根被从 registeredRoots 和 DB 中删除——表现为"导入后重启文件夹消失"）。
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			time.Sleep(300 * time.Millisecond)
			if info2, err2 := os.Stat(root); err2 != nil || !info2.IsDir() {
				deadRoots = append(deadRoots, root)
				continue
			}
		}

		if totalFolders == 0 {
			rootsToScan = append(rootsToScan, root)
			continue
		}

		a.mu.RLock()
		rootNorm := strings.ReplaceAll(root, "\\", "/")
		_, hasCount := a.folderCount[rootNorm]
		a.mu.RUnlock()

		if !hasCount {
			a.mu.RLock()
			hasImages := false
			for key, c := range a.folderCount {
				if c > 0 && strings.HasPrefix(key, rootNorm+"/") {
					hasImages = true
					break
				}
			}
			a.mu.RUnlock()
			if !hasImages {
				rootsToScan = append(rootsToScan, root)
			}
		}
	}

	// ★ 修复：把上次中断扫描的根目录加入补扫列表。
	//   中断扫描可能留下非零但不全的计数（快速路径会误判为完整），
	//   refreshFolderInternal 增量对比可安全补齐缺失部分。
	for _, root := range a.getInterruptedScans() {
		already := false
		for _, r := range rootsToScan {
			if r == root {
				already = true
				break
			}
		}
		if already {
			continue
		}
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			rootsToScan = append(rootsToScan, root)
			fmt.Printf("[启动修复] 上次扫描被中断，补扫: %s\n", root)
		} else {
			// 目录已不存在，清除残留标记（deadRoots 清理会处理注册）
			a.setScanInProgress(root, false)
		}
	}

	if len(rootsToScan) > 0 {
		if totalFolders == 0 {
			fmt.Printf("[启动修复] folderIndex 为空但已注册 %d 个目录，触发增量扫描\n", len(rootsToScan))
		} else {
			fmt.Printf("[启动修复] 发现 %d 个文件夹缺失索引，开始增量补扫\n", len(rootsToScan))
		}
		// 使用 refreshFolderInternal 代替 scanRootAsync
		// 缓存为空时它会把所有文件当新增（等同于全量扫描，但只走一遍）
		// 缓存有时它做增量对比，更轻量
		for _, root := range rootsToScan {
			// 通知前端开始扫描
			if a.ctx != nil {
				wailsruntime.EventsEmit(a.ctx, "scan:start", map[string]interface{}{
					"rootPath": root,
				})
			}
			fmt.Printf("[启动修复] 增量扫描: %s\n", root)
			result := a.refreshFolderInternal(root)
			if !result.Success {
				fmt.Printf("[启动修复] 跳过 %s: %s\n", root, result.Error)
			} else {
				fmt.Printf("[启动修复] %s: +%d 张, -%d 张, %d 张未变\n",
					filepath.Base(root), len(result.Added), len(result.Removed), result.Unchanged)
			}
		}
		a.saveImageIndex()
		fmt.Printf("[启动修复] 补扫完成，共处理 %d 个根目录\n", len(rootsToScan))
	}

	// ★ 清理已不存在的孤立根目录
	if len(deadRoots) > 0 {
		a.mu.Lock()
		for _, root := range deadRoots {
			delete(a.registeredRoots, root)
			delete(a.folderTypes, root)
		}
		// 重建 roots 列表用于持久化
		roots := make([]database.ImportedRoot, 0, len(a.registeredRoots))
		for r := range a.registeredRoots {
			roots = append(roots, database.ImportedRoot{
				Path:       r,
				FolderType: a.folderTypes[r],
			})
		}
		a.mu.Unlock()
		if a.userDataDB != nil && len(roots) > 0 {
			if err := a.userDataDB.SaveRoots(roots); err != nil {
				fmt.Printf("[启动修复] 持久化根目录失败: %v\n", err)
			}
		}
		fmt.Printf("[启动修复] 已清理 %d 个不存在的根目录: %v\n", len(deadRoots), deadRoots)
	}
}

// startupIncrementalRefresh 启动时对每个已注册根目录做增量检查
// 对比磁盘文件与缓存，只处理新增和删除的图片，不重扫已有文件
func (a *App) startupIncrementalRefresh() {
	if a.bgPaused.Load() == 1 {
		return
	}
	a.mu.RLock()
	roots := make([]string, 0, len(a.registeredRoots))
	for root := range a.registeredRoots {
		roots = append(roots, root)
	}
	a.mu.RUnlock()

	if len(roots) == 0 {
		return
	}

	fmt.Printf("[启动刷新] 对 %d 个已注册目录进行增量检查...\n", len(roots))

	totalAdded, totalRemoved := 0, 0
	for _, root := range roots {
		result := a.refreshFolderInternal(root)
		if !result.Success {
			fmt.Printf("[启动刷新] 跳过 %s: %s\n", root, result.Error)
			continue
		}
		if len(result.Added) > 0 || len(result.Removed) > 0 {
			fmt.Printf("[启动刷新] %s: +%d 张, -%d 张, %d 张未变\n",
				filepath.Base(root), len(result.Added), len(result.Removed), result.Unchanged)
		}
		totalAdded += len(result.Added)
		totalRemoved += len(result.Removed)

		// 每个根目录刷新完成后通知前端
		if a.ctx != nil {
			rootNorm := strings.ReplaceAll(root, "\\", "/")
			thumbCount := 0
			if thumbCounts := a.getCachedThumbCounts(); thumbCounts != nil {
				thumbCount = thumbCounts[rootNorm]
			}
			wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
				"rootPath":   root,
				"count":      len(result.Added) + result.Unchanged,
				"added":      len(result.Added),
				"removed":    len(result.Removed),
				"thumbCount": thumbCount,
			})
		}
	}

	if totalAdded > 0 || totalRemoved > 0 {
		fmt.Printf("[启动刷新] 完成：新增 %d 张，移除 %d 张\n", totalAdded, totalRemoved)
		a.saveImageIndex()
	} else {
		fmt.Printf("[启动刷新] 所有目录均为最新，无变化\n")
	}
}

// RefreshAll 供前端刷新按钮调用：执行与启动时相同的完整刷新周期
func (a *App) RefreshAll() map[string]interface{} {
	fmt.Println("[RefreshAll] 前端触发全量刷新（后台执行）...")
	// ★ 修复：后台执行，避免阻塞 RPC。大图库增量刷新（逐根目录走盘 + 批量落库）
	//   可能耗时数分钟，同步执行会让前端一直转圈。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("[RefreshAll] PANIC: %v\n", r)
			}
		}()
		a.ensureImageIndex()
		a.startupIncrementalRefresh()
		fmt.Println("[RefreshAll] 全量刷新完成")
		// ★ 通知前端刷新周期结束：可停止按钮旋转、提示完成。
		//   刷新期间的增量变化已由各根目录的 scan:complete 事件逐条推送给前端。
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "refresh:complete", map[string]interface{}{})
		}
	}()
	return map[string]interface{}{"success": true}
}

// ==================== 增量刷新 ====================

// refreshFolderInternal 对指定文件夹做增量对比，只处理新增和删除的图片。
// folderPath 是文件系统路径（如 D:\AIImages\sub）。
//
// 锁策略：
//
//	Phase 1 — IO（Walk + getImageDimensions），无锁
//	Phase 2 — SQLite 写入（a.imageDB）
//	Phase 3 — 内存更新（a.mu.Lock）
//	Phase 4 — JSON 持久化（a.saveImageIndex）
//
// stableID 基于 filePath+fileSize+fileModified 的 MD5。
// 已知限制：文件被移动/重命名会判定为删除+新增，丢失已有 metadata。
func (a *App) refreshFolderInternal(folderPath string) *FolderDiffResult {
	normalizedFolder := strings.ReplaceAll(folderPath, "\\", "/")

	// 找到包含此文件夹的已注册根目录（★ 取最长匹配，兼容嵌套目录被单独注册为根的情况）
	var matchedRoot string
	var bestRootNorm string
	a.mu.RLock()
	for root := range a.registeredRoots {
		rootNorm := strings.ReplaceAll(root, "\\", "/")
		if normalizedFolder == rootNorm {
			matchedRoot = root
			bestRootNorm = rootNorm
			break
		}
		if strings.HasPrefix(normalizedFolder, rootNorm+"/") && len(rootNorm) > len(bestRootNorm) {
			bestRootNorm = rootNorm
			matchedRoot = root
		}
	}
	a.mu.RUnlock()
	if matchedRoot == "" {
		return &FolderDiffResult{Success: false, Error: "未找到匹配的已注册根目录: " + folderPath}
	}

	// ★ 正在被快速导入/全量扫描的根目录：跳过本次刷新。
	//   否则 refreshFolderInternal 与在跑的扫描并发改同一批 memory/SQLite
	//   （快照→删→写回→rebuildFolderCountsFromSQLLocked），会把导入中的
	//   folderIndex/计数/SQLite 冲成残缺，表现为"导入中点刷新后文件夹消失/计数回退"。
	a.scanMu.Lock()
	beingScanned := a.scanningRoots[matchedRoot]
	a.scanMu.Unlock()
	if beingScanned {
		fmt.Printf("[增量刷新] 根目录正在扫描中，跳过刷新: %s\n", matchedRoot)
		return &FolderDiffResult{Success: false, Error: "文件夹正在扫描中，请稍后再刷新"}
	}

	// 检查文件夹是否存在
	info, err := os.Stat(folderPath)
	if err != nil || !info.IsDir() {
		return &FolderDiffResult{Success: false, Error: "文件夹不存在: " + folderPath}
	}

	// ★ 标记扫描中；defer 清除覆盖所有返回路径。
	//   启动补扫/设置重扫中途退出时标记残留 → 下次启动补扫，计数不会停在半成品。
	a.setScanInProgress(folderPath, true)
	defer a.setScanInProgress(folderPath, false)

	// === Phase 1: IO 阶段（无锁）===

	// 1a. 快照：拷贝 folderIndex 中属于此文件夹树的 ID 集合（短暂读锁）
	//   ★ 同时构建 "真实目录路径 -> id 集合" 的映射，供叶子目录 mtime 跳过时标记"文件仍在"。
	a.mu.RLock()
	oldIDs := make(map[string]bool)
	dirIDMap := make(map[string]map[string]bool) // realDirNorm -> id set
	mtRootNorm := strings.ReplaceAll(matchedRoot, "\\", "/")
	for folderKey, ids := range a.folderIndex {
		if folderKey == normalizedFolder || strings.HasPrefix(folderKey, normalizedFolder+"/") {
			for _, id := range ids {
				oldIDs[id] = true
			}
			// folderKey 形如 "rootNorm/rel" → 还原真实目录
			rel := ""
			if len(folderKey) > len(mtRootNorm) {
				rel = folderKey[len(mtRootNorm)+1:]
			}
			realDir := matchedRoot
			if rel != "" {
				realDir = matchedRoot + "\\" + strings.ReplaceAll(rel, "/", "\\")
			}
			realDirNorm := strings.ReplaceAll(realDir, "\\", "/")
			if dirIDMap[realDirNorm] == nil {
				dirIDMap[realDirNorm] = make(map[string]bool)
			}
			for _, id := range ids {
				dirIDMap[realDirNorm][id] = true
			}
		}
	}
	a.mu.RUnlock()

	// ★ 修复：以磁盘为权威补全 oldIDs —— 把 image_cache 表中属于此文件夹子树的记录也纳入对比。
	//   此前 oldIDs 只来自内存 folderIndex：若某记录已从内存/搜索索引清出，但 image_cache 表里
	//   仍残留（源文件删除后的孤儿），刷新永远检测不到它，计数与图廊会一直带着"指向不存在文件"
	//   的记录（表现为刷新显示"移除 0"但计数/破图不变）。补上表里的 ID 后，下面 1c 的
	//   removedIDs = oldIDs - currentIDs 就能识别出孤儿并交给 Phase 2/3/4 清理。
	if a.imageDB != nil {
		dbIDs, err := a.imageDB.LoadImageCacheIDsUnderPath(normalizedFolder)
		if err != nil {
			fmt.Printf("[增量刷新] 读取 image_cache 子树记录失败 %s: %v\n", normalizedFolder, err)
		} else {
			for _, id := range dbIDs {
				oldIDs[id] = true
			}
		}
	}

	// 1b. Walk 文件夹，对每个图片文件计算 stableID 并做对比
	// ★ 修复：用 os.ReadDir 递归替代 filepath.Walk（后者不跟随 Windows 目录联接、
	//   长路径会静默跳过 → 每次刷新都只索引到一部分，计数对不上总数）。
	//   已存在的文件跳过尺寸读取（避免全量 IO）。
	currentIDs := make(map[string]bool)
	var addedEntries []*ImageEntry
	unchanged := 0

	var walkFn func(dir string, rel string)
	walkFn = func(dir string, rel string) {
		items, readErr := os.ReadDir(dir)
		if readErr != nil {
			return
		}

		// ★ 目录 mtime 缓存：记录当前 mtime；若为"叶子目录（无子目录）"且 mtime 未变，
		//   说明里面一个文件都没增删改 → 整目录跳过，不再对每个文件 stat/MD5，
		//   把刷新从 O(所有文件) 降为 O(有变化的目录)。叶子目录无子目录，因此
		//   "叶子 mtime 未变 ⇒ 其内文件未变"，跳过是安全的（不会漏掉子目录变化）。
		dirNorm := strings.ReplaceAll(dir, "\\", "/")
		var dm int64
		if dInfo, err := os.Stat(dir); err == nil {
			dm = dInfo.ModTime().UnixMilli()
		}
		hasSubdir := false
		for _, it := range items {
			if it.IsDir() {
				hasSubdir = true
				break
			}
		}
		a.mu.Lock()
		leafUnchanged := !hasSubdir && a.dirMtimeUnchanged(dirNorm, dm)
		a.recordDirMtime(dirNorm, dm)
		a.mu.Unlock()
		if leafUnchanged {
			// 叶子目录未变：其文件都还在，标记为 current（避免被当作删除）。
			// 仅当能映射到该目录的已知文件时才跳过；映射不到（如嵌套根路径不一致）
			// 则回退完整处理，保证不会误删。
			// ★ 修复：folderIndex 键存在但值为 nil（"未加载"占位——重启后 loadFolderIndexLight
			//   对每个键都设 nil）时，dirIDMap 里该目录映射到"空集合"。此时若跳过，会把
			//   该目录的文件全部漏掉，SQL 侧 oldIDs（image_cache 残留）被当作"已删除"清掉，
			//   表现为"刷新后整目录图片消失"。只有映射到已知文件（len(ids)>0）才可安全跳过；
			//   映射为空则回退完整处理：文件仍在 a.images 会被标记 current 保留，否则重扫入库。
			if ids, mapped := dirIDMap[dirNorm]; mapped && len(ids) > 0 {
				// ★ 完整性校验：若该叶子目录里实际媒体文件数 ≠ 已索引数，说明上次扫描
				//   是半成品（导入中途中断/被并发干扰），mtime 未变但仍有文件未入库——
				//   此时不能跳过，必须重扫这一目录，否则"点刷新计数一直不变"。
				itemCount := 0
				for _, it := range items {
					if !it.IsDir() && (isImageFile(it.Name()) || isVideoFile(it.Name())) {
						itemCount++
					}
				}
				if itemCount == len(ids) {
					for id := range ids {
						currentIDs[id] = true
					}
					return
				}
			}
		}

		for _, item := range items {
			if item.IsDir() {
				subRel := item.Name()
				if rel != "" {
					subRel = rel + "/" + item.Name()
				}
				walkFn(filepath.Join(dir, item.Name()), subRel)
				continue
			}
			if !isImageFile(item.Name()) && !isVideoFile(item.Name()) {
				continue
			}
			fullPath := filepath.Join(dir, item.Name())
			info, infoErr := item.Info()
			if infoErr != nil {
				continue
			}
			id := generateStableID(fullPath, info.Size(), info.ModTime().UnixMilli())
			currentIDs[id] = true

			// 已存在 → 跳过（不重复读尺寸）
			a.mu.RLock()
			_, exists := a.images[id]
			a.mu.RUnlock()
			if exists {
				unchanged++
				continue
			}

			// 新文件 → 获取尺寸并创建条目
			isVideo := isVideoFile(item.Name())
			w, h := 0, 0
			// ★ 扫描阶段不逐张读取图片尺寸（见上文注释，GetImages 懒加载兜底）
			relFolder := rel
			if relFolder == "." {
				relFolder = ""
			}
			addedEntries = append(addedEntries, &ImageEntry{
				ID:           id,
				Path:         fullPath,
				Name:         item.Name(),
				Size:         info.Size(),
				LastModified: info.ModTime().UnixMilli(),
				CreatedAt:    getFileCreationTimeMillis(fullPath, info),
				Folder:       relFolder,
				RootPath:     matchedRoot,
				URL:          fmt.Sprintf("/image/%s", id),
				Width:        w,
				Height:       h,
				IsVideo:      isVideo,
			})
		}
	}
	// ★ 支持刷新子文件夹：rel 从该文件夹相对其根目录的路径开始，
	//   否则直接传子文件夹会导致新条目的 folder 字段丢失"根→该文件夹"的路径段。
	matchedRootNorm := strings.ReplaceAll(matchedRoot, "\\", "/")
	relBase := ""
	if normalizedFolder != matchedRootNorm {
		relBase = normalizedFolder[len(matchedRootNorm)+1:]
	}
	walkFn(folderPath, relBase)

	// ★ 持久化目录 mtime 缓存：本次刷新记录的 mtime 落盘，重启后不必重扫未变的叶子目录。
	a.saveDirMtimes()

	// 1c. 计算删除的 ID：oldIDs 中不在 currentIDs 里的
	var removedIDs []string
	for id := range oldIDs {
		if !currentIDs[id] {
			removedIDs = append(removedIDs, id)
		}
	}

	if len(addedEntries) == 0 && len(removedIDs) == 0 {
		// ★ 即使没有增删，也重建一次该根目录计数：可能上次导入/落库被并发扫描干扰，
		//   导致 folderCount 停留在偏小/0 值（表现为"点刷新计数一直不变"）。数据已完整，
		//   从 SQLite 重建一次最稳妥。
		a.mu.Lock()
		a.rebuildFolderCountsFromSQLLocked()
		a.mu.Unlock()
		return &FolderDiffResult{Added: []SafeImage{}, Removed: []string{}, Unchanged: unchanged, Success: true}
	}

	// === Phase 2: SQLite 持久化（在内存更新之前，利用 ON CONFLICT 保证幂等）===

	if a.imageDB != nil {
		if len(removedIDs) > 0 {
			if err := a.imageDB.DeleteImagesBatch(removedIDs); err != nil {
				fmt.Printf("[增量刷新] SQLite 删除失败: %v\n", err)
			}
		}
		if len(addedEntries) > 0 {
			// ★ 修复：批量插入（原为逐条事务，大文件夹补扫时每条一个事务极慢，
			//   导致"刷新后计数迟迟不更新 / 像是一次只加一部分"）
			records := make([]*database.ImageRecord, 0, len(addedEntries))
			for _, entry := range addedEntries {
				records = append(records, &database.ImageRecord{
					ID:           entry.ID,
					Path:         entry.Path,
					Name:         entry.Name,
					Size:         entry.Size,
					LastModified: entry.LastModified,
					CreatedAt:    entry.CreatedAt,
					Folder:       entry.Folder,
					RootPath:     entry.RootPath,
				})
			}
			// ★ 分批插入（每批 ~2000 条）：IndexBatch 内部持有 ImageDB 单一互斥锁，
			//   一次性插入数万条会让 GetImages/缩略图路径解析长时间阻塞（表现为"不出图"）。
			//   分批后每批之间释放锁，前台浏览请求可插队。
			const insertBatchSize = 2000
			for i := 0; i < len(records); i += insertBatchSize {
				end := i + insertBatchSize
				if end > len(records) {
					end = len(records)
				}
				if _, err := a.imageDB.IndexBatch(records[i:end]); err != nil {
					fmt.Printf("[增量刷新] SQLite 批量插入失败 (%d 条): %v\n", end-i, err)
					break
				}
			}
		}
	}

	// SQLite 写入成功后内存更新失败（极罕见）：
	// 持久化已完成，下次启动会自动恢复一致状态。
	// 调用方收到 error 可安全重试，SQL 操作（ON CONFLICT / DELETE）保证幂等。

	// === Phase 3: 内存更新（★ 分批持锁，避免长时间持有 a.mu 阻塞 GetFolders/GetImages） ===

	// 3a. 删除清理（数量通常较少，一次性持锁）
	a.mu.Lock()
	var emptiedKeys []string
	for _, id := range removedIDs {
		delete(a.images, id)
	}
	// 从 folderIndex 中移除已删除的 ID
	affectedKeys := make(map[string]bool)
	for folderKey, ids := range a.folderIndex {
		if folderKey == normalizedFolder || strings.HasPrefix(folderKey, normalizedFolder+"/") {
			affectedKeys[folderKey] = true
			var remaining []string
			for _, id := range ids {
				if _, ok := a.images[id]; ok {
					remaining = append(remaining, id)
				}
			}
			if len(remaining) == 0 {
				a.folderIndex[folderKey] = nil // 保留 key 占位
				emptiedKeys = append(emptiedKeys, folderKey)
			} else {
				a.folderIndex[folderKey] = remaining
			}
		}
	}
	a.mu.Unlock()

	// 3a2. ★ 幽灵文件夹清理：目录已不存在的空 folder key 彻底移除（IO 阶段不持锁）。
	//   若只保留 nil 占位，磁盘上已删除的文件夹会一直以空节点留在导航栏（表现为重复项）。
	if len(emptiedKeys) > 0 {
		var deadEmptyKeys []string
		for _, k := range emptiedKeys {
			if k == normalizedFolder {
				continue // 刷新目标自身目录已在上文校验存在，保留占位
			}
			rel := strings.TrimPrefix(k, normalizedFolder+"/")
			diskDir := filepath.Join(folderPath, filepath.FromSlash(rel))
			if info, err := os.Stat(diskDir); err != nil || !info.IsDir() {
				deadEmptyKeys = append(deadEmptyKeys, k)
			}
		}
		if len(deadEmptyKeys) > 0 {
			a.mu.Lock()
			for _, k := range deadEmptyKeys {
				delete(a.folderIndex, k)
				delete(a.folderCount, k)
			}
			a.mu.Unlock()
		}
	}

	// 3b. 新增条目分批加入内存（每批 ~2000 条，之间释放 a.mu）
	const memBatchSize = 2000
	for i := 0; i < len(addedEntries); i += memBatchSize {
		end := i + memBatchSize
		if end > len(addedEntries) {
			end = len(addedEntries)
		}
		a.mu.Lock()
		for _, entry := range addedEntries[i:end] {
			a.images[entry.ID] = entry
			rootNorm := strings.ReplaceAll(entry.RootPath, "\\", "/")
			folderKey := rootNorm
			if entry.Folder != "" {
				folderKey = rootNorm + "/" + strings.ReplaceAll(entry.Folder, "\\", "/")
			}
			a.folderIndex[folderKey] = append(a.folderIndex[folderKey], entry.ID)
			affectedKeys[folderKey] = true
		}
		a.mu.Unlock()
	}

	// 3c. 与 LRU 协同：增量刷新结果即权威数据，标记受影响 folderKey 为 loaded
	a.mu.Lock()
	for fk := range affectedKeys {
		if !a.folderLoaded[fk] {
			a.folderLoaded[fk] = true
			a.lruNodes[fk] = a.folderLRU.PushBack(fk)
		} else {
			a.touchFolderLocked(fk)
		}
	}
	a.mu.Unlock()

	// === Phase 4: 先落库（此时 a.images 完整），再 LRU 逐出 ===
	// ★ 修复：不要在落库前 evictLRU！LRU 容量只有 32 个文件夹/5 万张图，
	//   大文件夹（子目录多或图片多）的图片会被立即淘汰出 a.images，
	//   导致落库时只写回残缺子集 → 计数永远对不上。与 scanRootAsync 一致：先落库再逐出。
	if normalizedFolder == matchedRootNorm {
		// 根目录：整体 DELETE-then-replace（原逻辑，统一处理删除+新增）
		// ★ 修复：先显式删除本次识别出的孤儿（image_cache 表残留、磁盘已无源文件的记录）。
		//   仅靠 saveImageIndexByRoot 的 DELETE-then-replace 依赖 a.images 完整；若 a.images
		//   恰好为空导致其跳过落库，孤儿会继续残留。先删一次保证 root 刷新一定能清干净。
		if a.imageDB != nil && len(removedIDs) > 0 {
			if err := a.imageDB.DeleteImageCacheBatch(removedIDs); err != nil {
				fmt.Printf("[增量存储] 根目录删除 image_cache 孤儿失败 (%d 条): %v\n", len(removedIDs), err)
			}
		}
		a.saveImageIndexForRoot(folderPath)
	} else {
		// ★ 子文件夹：局部增量落库（upsert 新增 + 删除移除的），不触碰根目录其它文件夹
		if a.imageDB != nil {
			if len(removedIDs) > 0 {
				if err := a.imageDB.DeleteImageCacheBatch(removedIDs); err != nil {
					fmt.Printf("[增量存储] 子文件夹删除 image_cache 失败 (%d 条): %v\n", len(removedIDs), err)
				}
			}
			if len(addedEntries) > 0 {
				entries := make([]database.ImageCacheEntry, 0, len(addedEntries))
				for _, entry := range addedEntries {
					entries = append(entries, database.ImageCacheEntry{
						ID: entry.ID, Path: entry.Path, Name: entry.Name, Size: entry.Size,
						LastModified: entry.LastModified, CreatedAt: entry.CreatedAt,
						Folder: entry.Folder, RootPath: entry.RootPath,
						Width: entry.Width, Height: entry.Height, IsVideo: entry.IsVideo,
					})
				}
				if err := a.imageDB.SaveImageCacheBatch(entries); err != nil {
					fmt.Printf("[增量存储] 子文件夹写入 image_cache 失败 (%d 条): %v\n", len(entries), err)
				}
			}
		}
	}
	// ★ 数据提交后才失效预览缓存：无变化的刷新（提前 return）不失效，
	//   避免每次右键刷新后 GetFolders 对全部预览文件夹重查（COUNT+OFFSET 拖慢树刷新）
	a.invalidatePreviewCache()
	// 数据变更：生成参数标签缓存一并失效
	a.invalidateParamTags()
	a.mu.Lock()
	a.evictLRU()
	a.rebuildFolderCountsFromSQLLocked()
	a.mu.Unlock()

	// ★ 增量刷新完成：自动低优预生成新增图片的缺失缩略图（后台执行，不阻塞返回）
	if len(addedEntries) > 0 {
		go a.triggerAutoPreGen(filepath.Base(folderPath), addedEntries)
	}

	// 构造返回结果
	addedSafe := make([]SafeImage, len(addedEntries))
	for i, entry := range addedEntries {
		addedSafe[i] = SafeImage{
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
			Width:        entry.Width,
			Height:       entry.Height,
			IsVideo:      entry.IsVideo,
		}
	}

	fmt.Printf("[增量刷新] 完成: %s, 新增 %d, 删除 %d, 未变 %d\n",
		folderPath, len(addedEntries), len(removedIDs), unchanged)

	return &FolderDiffResult{
		Added:     addedSafe,
		Removed:   removedIDs,
		Unchanged: unchanged,
		Success:   true,
	}
}

// autoHealMissingFolder 自愈 GetImages 返回 0 的场景：目录在磁盘上仍存在且属于已注册
// 根目录，但 image_cache / 内存索引都没有它的记录（历史 bug 误删、中断扫描残留等）。
// 后台对该文件夹做一次增量刷新，把磁盘上的文件重新索引回来；有变化时发 scan:complete
// 让前端自动重拉当前文件夹（无变化不发事件，避免空目录被反复点击造成重拉循环）。
func (a *App) autoHealMissingFolder(normalizedFolder string) {
	if a.bgPaused.Load() == 1 {
		return
	}
	// 找到包含该文件夹的最长匹配根目录（GetImages 只关心"确实属于已导入范围"）
	a.mu.RLock()
	var matchedRoot string
	for root := range a.registeredRoots {
		rootNorm := strings.ReplaceAll(root, "\\", "/")
		if normalizedFolder == rootNorm || strings.HasPrefix(normalizedFolder, rootNorm+"/") {
			if len(rootNorm) > len(strings.ReplaceAll(matchedRoot, "\\", "/")) {
				matchedRoot = root
			}
		}
	}
	a.mu.RUnlock()
	if matchedRoot == "" {
		return
	}
	// 并发去重：同一文件夹只允许一个自愈刷新在跑
	a.scanMu.Lock()
	if a.scanningRoots == nil {
		a.scanningRoots = make(map[string]bool)
	}
	if a.scanningRoots[normalizedFolder] {
		a.scanMu.Unlock()
		return
	}
	a.scanningRoots[normalizedFolder] = true
	a.scanMu.Unlock()

	fmt.Printf("[自愈] GetImages=0 但目录存在，后台补扫: %s\n", normalizedFolder)
	go func() {
		defer func() {
			a.scanMu.Lock()
			delete(a.scanningRoots, normalizedFolder)
			a.scanMu.Unlock()
		}()
		result := a.refreshFolderInternal(normalizedFolder)
		if !result.Success {
			fmt.Printf("[自愈] 后台补扫失败 %s: %s\n", normalizedFolder, result.Error)
			return
		}
		if len(result.Added) == 0 && len(result.Removed) == 0 {
			return // 目录确实为空，不发事件，避免前端反复重拉
		}
		fmt.Printf("[自愈] %s: 重新索引 %d 张、移除 %d 张\n",
			normalizedFolder, len(result.Added), len(result.Removed))
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
				"rootPath": matchedRoot,
				"count":    len(result.Added) + result.Unchanged,
				"added":    len(result.Added),
				"removed":  len(result.Removed),
			})
		}
	}()
}
