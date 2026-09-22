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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rwcarlsen/goexif/exif"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	_ "golang.org/x/image/webp"

	"local-gallery/internal/database"
)

// ==================== 扫描功能 ====================
// ★ 改造（M2）：磁盘扫描只做 diff → 直接写 SQLite（image_cache + folders 计数）。
//   不再做内存合并（a.images / folderIndex / folderCount 已删除），
//   读取侧（M3）全部走 SQL 查询。

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

	// ★ 排除嵌套虚拟根：其内容已由父目录扫描持有（图片记在父根 root_path 下），
	//   独立扫描会把同一批文件重复处理。
	a.mu.RLock()
	roots := make([]string, 0, len(a.registeredRoots))
	for r := range a.registeredRoots {
		if a.isRootNestedLocked(r) {
			continue
		}
		roots = append(roots, r)
	}
	a.mu.RUnlock()

	if len(roots) == 0 {
		fmt.Printf("[扫描] 没有已注册的根目录，跳过扫描\n")
		return 0
	}

	// ★ 每个 root 串行走增量 diff 扫描：scanRootAsync 按目录分批 upsert 到 SQLite
	//   并发 scan:batch 事件；已存在的文件幂等跳过，只处理新增/变化/删除。
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
		// 跳过"正在被其它扫描(如快速导入)处理"的根目录
		a.scanMu.Lock()
		beingScanned := a.scanningRoots[normalizedPath]
		a.scanMu.Unlock()
		if beingScanned {
			fmt.Printf("[扫描] 跳过正在扫描的根目录(避免并发抢写): %s\n", normalizedPath)
			continue
		}
		totalCount += a.scanRootAsync(normalizedPath)
		scannedAny = true
	}

	if !scannedAny {
		fmt.Printf("[扫描] 没有实际扫描的根目录(均在扫描中/无效)，跳过收尾\n")
		return 0
	}

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

// getImageDimensions 读取图片宽高（考虑 EXIF 旋转方向）。
// ★ 快路径：fastImageDimensions 只读 ≤1MB 文件头解析（<1ms），失败才走
//   DecodeConfig + goexif 慢路径（再失败由其内部回退 vips）。
//   慢盘实测旧路径 ~920ms/张，快路径把首屏懒加载和后台回填的成本降低约百倍。
func getImageDimensions(filePath string) (int, int) {
	if w, h := fastImageDimensions(filePath); w > 0 && h > 0 {
		return w, h
	}
	return getImageDimensionsSlow(filePath)
}

// getImageDimensionsSlow 旧实现：DecodeConfig 头部解码 + goexif 全量解析，
// 作为 fastImageDimensions 的兜底（罕见 JPEG 变体/HEIC 等），内部再回退 vips。
func getImageDimensionsSlow(filePath string) (int, int) {
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

// scanWalkBatched 按目录粒度增量扫描，每个目录完成后回调一次。
// onBatch 收到该目录的图片条目（已含 stableID/相对 folder/root），由调用方写入 SQLite。
func (a *App) scanWalkBatched(rootPath string, onBatch func(entries []database.ImageCacheEntry, folderRel string, count int)) {
	a.scanWalkDirBatched(rootPath, rootPath, "", onBatch)
}

// scanWalkDirBatched 递归遍历，每层目录扫描完后立即回调。
func (a *App) scanWalkDirBatched(dirPath, rootPath, currentRel string, onBatch func(entries []database.ImageCacheEntry, folderRel string, count int)) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		fmt.Printf("[扫描] 跳过目录 %s: %v\n", dirPath, err)
		return
	}

	// ★ 记录目录 mtime：扫描/导入时顺带把各目录修改时间记入缓存，
	//   使"导入后第一次刷新"也能直接跳过未变的叶子目录，不必全量重扫。
	if dInfo, err := os.Stat(dirPath); err == nil {
		a.mu.Lock()
		a.recordDirMtime(strings.ReplaceAll(dirPath, "\\", "/"), dInfo.ModTime().UnixMilli())
		a.mu.Unlock()
	}

	var batch []database.ImageCacheEntry

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
		// ★ 扫描阶段不逐张读取图片尺寸：getImageDimensions 需解码头部 + 读整个文件解析
		//   EXIF，在图片多/慢盘上会拖慢甚至卡住扫描。尺寸由 GetImages 懒加载 +
		//   backfillImageDimensions 后台回填写库。
		batch = append(batch, database.ImageCacheEntry{
			ID:           id,
			Path:         fullPath,
			Name:         entry.Name(),
			Size:         info.Size(),
			LastModified: info.ModTime().UnixMilli(),
			CreatedAt:    getFileCreationTimeMillis(fullPath, info),
			Folder:       relFolder,
			RootPath:     rootPath,
			IsVideo:      isVideoFile(entry.Name()),
		})
	}

	if len(batch) > 0 {
		onBatch(batch, currentRel, len(batch))
	}
}

// writeScanBatchToDB 把一批扫描条目 upsert 进 image_cache 并增量维护 folders 计数。
// 幂等：已存在的条目只更新基础字段，不重复计数。
func (a *App) writeScanBatchToDB(root string, entries []database.ImageCacheEntry) {
	if a.imageDB == nil || len(entries) == 0 {
		return
	}
	if err := a.imageDB.SaveImageCacheBatchCounted(root, entries); err != nil {
		fmt.Printf("[扫描] 写入 image_cache 失败 (%d 条): %v\n", len(entries), err)
	}
}

func (a *App) removeByRoot(rootPath string) {
	// 收集要删除的图片 ID（按路径前缀查 DB），用于清理缩略图
	var idsToRemove []string
	if a.imageDB != nil {
		rootNorm := strings.ReplaceAll(rootPath, "\\", "/")
		if entries, err := a.imageDB.LoadImageCacheByPathPrefix(rootNorm); err == nil {
			for _, e := range entries {
				idsToRemove = append(idsToRemove, e.ID)
			}
		}
	}

	// 清理 image_cache / 搜索索引 / folders 表
	if a.imageDB != nil {
		if deleted, err := a.imageDB.DeleteImageCacheByRoot(rootPath); err != nil {
			fmt.Printf("[清理] 从数据库删除缓存记录失败 [%s]: %v\n", rootPath, err)
		} else if deleted > 0 {
			fmt.Printf("[清理] 已从数据库删除 %d 条缓存记录 [%s]\n", deleted, rootPath)
		}
		a.removeByRootFromDB(rootPath)
		if err := a.imageDB.DeleteFoldersByRoot(rootPath); err != nil {
			fmt.Printf("[清理] 从 folders 表删除记录失败 [%s]: %v\n", rootPath, err)
		}
	}

	// 清理对应的缩略图缓存
	a.removeThumbsByIDs(idsToRemove)
	// ★ 同步清掉该根目录（含子路径）的缩略图计数缓存：否则重新导入后
	//   缩略图计数会在旧值上继续累加，表现为"数量翻倍 / 只重复一部分"。
	a.clearThumbCountsForRoot(rootPath)
	a.invalidatePreviewCache()
	a.invalidateParamTags()
}

// removeByRootFromDB 只清除搜索索引表中对应 root_path 的记录
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

// folderIsSubfolder 判断 normalizedFolder(正斜杠) 是否为某个已注册根目录内的"严格"子文件夹。
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
// 把结果直接写入 SQLite，让"点哪个先出哪个"，不用等整个后台大扫描扫到它。
// 以最长的已注册父根为 RootPath 基准，保证与 GetImages/GetFolderCount 的前缀匹配一致。
// 返回本次扫描到的条目数（0 = 无内容或不属于任何已注册根）。
// 与后台扫描并发去重（subfolderScanning）。
func (a *App) scanSubfolderImmediate(subfolder string) int {
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
		return 0 // 不位于任何已注册根内，或本身就是根，交给常规扫描
	}
	// ★ 按长度取相对路径，避免大小写不同导致 TrimPrefix 失败
	relFolder := normalized[len(parentRootNorm)+1:]
	if relFolder == "" {
		return 0
	}

	// 并发去重：同一子文件夹只允许一个即席扫描在跑
	a.scanMu.Lock()
	if a.subfolderScanning == nil {
		a.subfolderScanning = make(map[string]bool)
	}
	if a.subfolderScanning[normalized] {
		a.scanMu.Unlock()
		return 0
	}
	a.subfolderScanning[normalized] = true
	a.scanMu.Unlock()
	defer func() {
		a.scanMu.Lock()
		delete(a.subfolderScanning, normalized)
		a.scanMu.Unlock()
	}()

	fmt.Printf("[即席扫描] 点开子文件夹，立即扫描入库: %s (父根: %s)\n", normalized, parentRoot)
	total := 0
	a.scanWalkDirBatched(subfolder, parentRoot, relFolder,
		func(entries []database.ImageCacheEntry, folderRel string, count int) {
			a.writeScanBatchToDB(parentRoot, entries)
			total += count
		})
	if total > 0 {
		a.invalidatePreviewCache()
	}
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "folder:count", map[string]interface{}{
			"folderPath": normalized,
			"count":      a.dbSubtreeCount(normalized),
		})
	}
	return total
}

// pruneDeadFolders 清理磁盘上已删除、但数据库仍残留的幽灵文件夹。
// 根源：启动时不做全量增量刷新，文件夹被删除后其 image_cache 记录与 folders 表行
// 会一直残留 → 导航栏出现同名重复项、图廊/预览缩略图指向不存在的文件而无法显示。
// 做法：用 folders 表的行作为"已索引文件夹"清单做目录存在性检查（IO 阶段不持锁），
// 目录已不存在的文件夹连同其图片记录从 image_cache/搜索索引/缩略图库中一并移除。
func (a *App) pruneDeadFolders() (prunedFolders int, prunedImages int) {
	a.mu.RLock()
	roots := make([]string, 0, len(a.registeredRoots))
	for r := range a.registeredRoots {
		roots = append(roots, r)
	}
	a.mu.RUnlock()
	if len(roots) == 0 || a.imageDB == nil {
		return 0, 0
	}

	// ★ 启动速度优化：只对"根目录 mtime 变了"的根做幽灵清理。健康启动（磁盘上无增删）
	//   直接跳过，避免每次开机对每个子文件夹目录 os.Stat。
	var changedRoots []string
	for _, root := range roots {
		rootNorm := strings.ReplaceAll(root, "\\", "/")
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
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

	// 1. 从 folders 表取已索引文件夹，检查磁盘存在性（IO 阶段不持锁）
	var deadKeys []string
	for _, root := range changedRoots {
		rows, err := a.imageDB.LoadFolderTree(root)
		if err != nil || len(rows) == 0 {
			continue
		}
		rootNorm := strings.ReplaceAll(root, "\\", "/")
		for _, row := range rows {
			if row.Folder == "" {
				continue
			}
			diskDir := filepath.Join(root, filepath.FromSlash(row.Folder))
			if info, err := os.Stat(diskDir); err != nil || !info.IsDir() {
				deadKeys = append(deadKeys, rootNorm+"/"+row.Folder)
			}
		}
	}
	if len(deadKeys) == 0 {
		return 0, 0
	}
	return a.pruneFolderKeys(deadKeys)
}

// pruneFolderKeys 将一组已确认不存在的文件夹 key（规范化完整路径）连同其图片记录，
// 从 image_cache、搜索索引、缩略图库中一并移除，并校准 folders 计数；
// 随后触发前端文件夹树刷新。
// 供"启动幽灵清理""打开已删除文件夹"等场景复用（同一套清理逻辑，防止同类问题回归）。
func (a *App) pruneFolderKeys(deadKeys []string) (prunedFolders int, prunedImages int) {
	// 1. 收集死文件夹下的全部图片 ID（SQLite）
	deadIDs := make(map[string]bool)
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

	// 3. 校准受影响根目录的 folders 计数（此时 DB 已无死记录）
	a.mu.RLock()
	affectedRoots := make(map[string]bool)
	for _, k := range deadKeys {
		for r := range a.registeredRoots {
			rn := strings.ReplaceAll(r, "\\", "/")
			if k == rn || strings.HasPrefix(k, rn+"/") {
				affectedRoots[r] = true
			}
		}
	}
	a.mu.RUnlock()
	for root := range affectedRoots {
		a.imageDB.RecomputeFolderCountsForRoot(root, nil)
	}
	a.invalidatePreviewCache()
	a.invalidateParamTags()

	prunedFolders = len(deadKeys)
	prunedImages = len(deadIDs)
	fmt.Printf("[幽灵清理] 移除 %d 个已不存在的文件夹、%d 张失效图片\n", prunedFolders, prunedImages)

	// 4. 通知前端刷新文件夹树（scan:complete 会触发 Sidebar.refreshFolderTree）
	if a.ctx != nil {
		for rn := range affectedRoots {
			wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
				"rootPath":   strings.ReplaceAll(rn, "\\", "/"),
				"count":      0,
				"added":      0,
				"removed":    prunedImages,
				"thumbCount": 0,
			})
		}
	}
	return prunedFolders, prunedImages
}

// purgeMissingImage 单张图片自愈：源文件已删除时移除其全部残留（DB、缩略图），
// 并增量修正 folders 计数。
// 由缩略图生成失败路径调用——文件夹仍在但其中文件被删的"文件级幽灵"，
// 会在被浏览到的那一刻自动清除，图廊不再残留破图。
func (a *App) purgeMissingImage(id string) {
	var entry *database.ImageCacheEntry
	if a.imageDB != nil {
		if e, err := a.imageDB.GetImageEntry(id); err == nil {
			entry = e
		}
		a.imageDB.DeleteImage(id)
		a.imageDB.DeleteImageCacheBatch([]string{id})
	}
	a.removeThumbsByIDs([]string{id})
	if entry != nil && a.imageDB != nil {
		rootNorm := strings.ReplaceAll(entry.RootPath, "\\", "/")
		folderRel := normalizeFolderRel(entry.Folder, rootNorm)
		a.imageDB.ApplyImageDelta(entry.RootPath, folderRel, -1)
	}
	fmt.Printf("[幽灵清理] 单张图片自愈移除: %s\n", id)
}

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

// owningRootLockedNorm 返回包含 norm 的最近（最长匹配）已注册根目录；
// 没有则返回空串。调用方需已持有 a.mu 读锁或写锁。
// 用于嵌套虚拟根：它的图片记在这个外层父根名下。
func (a *App) owningRootLockedNorm(norm string) string {
	best := ""
	bestLen := -1
	for root := range a.registeredRoots {
		rn := strings.ToLower(strings.ReplaceAll(root, "\\", "/"))
		if rn != norm && strings.HasPrefix(norm, rn+"/") && len(rn) > bestLen {
			best = root
			bestLen = len(rn)
		}
	}
	return best
}

// buildFolderTreeFromDB 从 folders 表行构建某根目录下的文件夹树（零磁盘 I/O）。
// rows 为该根的 LoadFolderTree 结果（含根行 folder=""）。
// ★ 嵌套根折叠语义与旧 buildFolderTreeFromIndex 一致：本根之内已注册的嵌套根
//   渲染为"折叠入口"——不展开其子文件夹（其子树行挂在父根的 folders 表里，
//   计数仍显示嵌套根路径处的全部图片数）。
func (a *App) buildFolderTreeFromDB(rootPath string, rows []database.FolderRow, thumbCounts map[string]int, previews map[string][]FolderPreview) []*FolderNode {
	normalizedRoot := strings.ReplaceAll(rootPath, "\\", "/")
	prefix := normalizedRoot + "/"

	if len(rows) == 0 {
		return []*FolderNode{}
	}

	// 本根之内已注册的嵌套根集合
	var registeredUnder []string
	a.mu.RLock()
	for root := range a.registeredRoots {
		rn := strings.ReplaceAll(root, "\\", "/")
		if rn != normalizedRoot && strings.HasPrefix(rn, prefix) {
			registeredUnder = append(registeredUnder, rn)
		}
	}
	a.mu.RUnlock()
	registeredSet := make(map[string]bool, len(registeredUnder))
	for _, rn := range registeredUnder {
		registeredSet[rn] = true
	}

	// 用 map 构建树节点，key 为规范化路径
	nodes := make(map[string]*FolderNode, len(rows))
	for _, row := range rows {
		if row.Folder == "" {
			continue // 根路径本身，根是 GetFolders 创建的
		}
		subPath := normalizedRoot + "/" + row.Folder
		if _, exists := nodes[subPath]; exists {
			continue
		}
		node := &FolderNode{
			Name:       row.Name,
			Path:       filepath.Join(rootPath, filepath.FromSlash(row.Folder)),
			ImageCount: row.SubtreeCount,
			ThumbCount: thumbCounts[subPath],
			Children:   nil,
		}
		// ★ 子文件夹缩略图预览：命中已开启预览的子树时，挂上预取的预览条目
		if ps, ok := previews[subPath]; ok && len(ps) > 0 {
			node.Previews = ps
		}
		nodes[subPath] = node
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
// SQLite 兜底路径（COUNT+OFFSET）用该算法，保证同一文件夹的预览选取稳定。
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
	atomic.AddInt32(&activeScanOps, 1)
	defer func() {
		atomic.AddInt32(&activeScanOps, -1)
		a.scanMu.Lock()
		delete(a.scanningRoots, rootPath)
		a.scanMu.Unlock()
	}()
	// ★ 持久化"扫描中"标记：扫描中途退出时残留，下次启动 ensureImageIndex 补扫
	a.setScanInProgress(rootPath, true)
	fmt.Printf("[后台扫描] 开始扫描: %s\n", rootPath)

	// 通知前端开始扫描
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "scan:start", map[string]interface{}{
			"rootPath": rootPath,
		})
	}

	// ★ 增量 diff 扫描：每个目录批次直接 upsert 进 SQLite（幂等），不再走内存合并。
	//   seenIDs 记录磁盘上仍在的 ID，扫完后与 DB 对差集删除已消失的文件。
	localImages := make(map[string]*ImageEntry)
	seenIDs := make(map[string]bool)
	totalCount := 0

	a.scanWalkBatched(rootPath, func(entries []database.ImageCacheEntry, folderRel string, count int) {
		a.mu.RLock()
		registered := a.registeredRoots[rootPath]
		a.mu.RUnlock()
		if !registered {
			return
		}
		a.writeScanBatchToDB(rootPath, entries)
		for _, e := range entries {
			seenIDs[e.ID] = true
			localImages[e.ID] = toImageEntry(e)
		}
		totalCount += count
		if a.ctx != nil && count > 0 {
			thumbCount := 0
			if thumbCounts := a.getCachedThumbCounts(); thumbCounts != nil {
				thumbCount = thumbCounts[strings.ReplaceAll(rootPath, "\\", "/")]
			}
			wailsruntime.EventsEmit(a.ctx, "scan:batch", map[string]interface{}{
				"rootPath":   rootPath,
				"folder":     folderRel,
				"count":      count,
				"totalSoFar": totalCount,
				"thumbCount": thumbCount,
			})
		}
	})

	a.mu.RLock()
	registered := a.registeredRoots[rootPath]
	a.mu.RUnlock()
	if !registered {
		fmt.Printf("[后台扫描] 根目录已被移除，丢弃扫描结果: %s\n", rootPath)
		a.setScanInProgress(rootPath, false)
		return 0
	}

	// ★ 删除磁盘上已不存在的记录：DB 中该根目录的 ID 集合 - 本次扫描见到的 ID
	if a.imageDB != nil {
		rootNorm := strings.ReplaceAll(rootPath, "\\", "/")
		if dbIDs, err := a.imageDB.LoadImageCacheIDsUnderPath(rootNorm); err == nil {
			var removed []string
			for _, id := range dbIDs {
				if !seenIDs[id] {
					removed = append(removed, id)
				}
			}
			if len(removed) > 0 {
				if err := a.imageDB.DeleteImageCacheBatch(removed); err != nil {
					fmt.Printf("[后台扫描] 删除已消失文件的 image_cache 记录失败 (%d 条): %v\n", len(removed), err)
				}
				if err := a.imageDB.DeleteImagesBatch(removed); err != nil {
					fmt.Printf("[后台扫描] 删除已消失文件的搜索索引记录失败 (%d 条): %v\n", len(removed), err)
				}
				a.removeThumbsByIDs(removed)
				fmt.Printf("[后台扫描] 清理已消失文件 %d 个: %s\n", len(removed), rootPath)
			}
		}
		// ★ 扫描结束统一校准 folders 计数（增量 delta 已在每批写入时维护，
		//   这里做一次幂等兜底，吸收并发扫描/删除造成的偏差）。
		a.imageDB.RecomputeFolderCountsForRoot(rootPath, nil)
	}
	// ★ 扫描完成，清除"扫描中"标记
	a.setScanInProgress(rootPath, false)

	a.invalidatePreviewCache()
	a.invalidateParamTags()

	// ★ 优先级：扫描已完成 → 先补齐缩略图（用户看图优先），索引排最后。
	//   索引会用最多 8 个 CPU 逐张读原图解析元数据 + 大 FTS 写入，
	//   与预生成/浏览并行时抢 CPU 和数据库锁（表现为导入后出图慢）。
	if totalCount > 0 && len(localImages) > 0 {
		entries := make([]*ImageEntry, 0, len(localImages))
		for _, e := range localImages {
			entries = append(entries, e)
		}
		label := filepath.Base(rootPath)
		go func() {
			a.triggerAutoPreGen(label, entries)
			a.runIndexWhenIdle(localImages, rootPath)
		}()
	}
	fmt.Printf("[后台扫描] 完成: %s，共 %d 张图片\n", rootPath, totalCount)
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
	rootsCopy := make([]string, 0, len(a.registeredRoots))
	for root := range a.registeredRoots {
		rootsCopy = append(rootsCopy, root)
	}
	a.mu.RUnlock()

	// 快速路径：没有已注册根目录，无需检查
	if len(rootsCopy) == 0 {
		return
	}

	var rootsToScan []string
	var deadRoots []string

	containsRoot := func(list []string, r string) bool {
		for _, x := range list {
			if x == r {
				return true
			}
		}
		return false
	}

	for _, root := range rootsCopy {
		// ★ 修复：对瞬时 os.Stat 失败做一次重试（慢盘/网络盘挂载延迟时可能误判"目录不存在"）。
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			time.Sleep(300 * time.Millisecond)
			if info2, err2 := os.Stat(root); err2 != nil || !info2.IsDir() {
				deadRoots = append(deadRoots, root)
				continue
			}
		}
	}

	// ★ 以 SQLite 为真相源：根目录在 image_cache 中无记录 → 需要补扫。
	//   嵌套虚拟根由父根持有（图片记在父根 root_path 下），跳过。
	if a.imageDB != nil {
		for _, root := range rootsCopy {
			if containsRoot(deadRoots, root) {
				continue
			}
			if a.isRootNested(root) {
				continue
			}
			if a.imageDB.CountByRoot(root) == 0 {
				rootsToScan = append(rootsToScan, root)
			}
		}
	}

	// ★ 把上次中断扫描的根目录加入补扫列表（refreshFolderInternal 增量对比可安全补齐）。
	for _, root := range a.getInterruptedScans() {
		if containsRoot(rootsToScan, root) || containsRoot(deadRoots, root) {
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
		fmt.Printf("[启动修复] 发现 %d 个根目录缺失索引/中断残留，开始增量补扫\n", len(rootsToScan))
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
// ★ 改造（M2）：纯 DB diff——
//
//	Phase 1 — IO：Walk 磁盘，对每个媒体文件计算 stableID，与 DB 中该子树的
//	          ID 集合（LoadImageCacheIDsUnderPath）对比得到新增/删除。
//	Phase 2 — SQLite 写入：删除 removedIDs，SaveImageCacheBatchCounted upsert 新增
//	          （同一事务内增量维护 folders 计数）。
//	Phase 3 — RecomputeFolderCountsForRoot 幂等校准计数。
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
	//   否则并发改同一批 SQLite 会把导入中的计数冲成残缺。
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
	a.setScanInProgress(folderPath, true)
	atomic.AddInt32(&activeScanOps, 1)
	defer func() {
		atomic.AddInt32(&activeScanOps, -1)
		a.setScanInProgress(folderPath, false)
	}()

	// === Phase 1: IO 阶段（无锁）===

	// 1a. DB 中属于此文件夹子树的 ID 集合（含磁盘已删但 DB 残留的孤儿）
	oldIDs := make(map[string]bool)
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

	// 1b. Walk 文件夹，对每个图片文件计算 stableID 并做对比。
	// ★ 用 os.ReadDir 递归（跟随 Windows 目录联接；filepath.WalkDir 不跟随且长路径静默跳过）。
	//   已存在的文件跳过尺寸读取（避免全量 IO）；stableID 只是路径哈希，开销极低。
	currentIDs := make(map[string]bool)
	var addedEntries []*ImageEntry
	unchanged := 0

	var walkFn func(dir string, rel string)
	walkFn = func(dir string, rel string) {
		items, readErr := os.ReadDir(dir)
		if readErr != nil {
			return
		}

		// 记录目录 mtime 缓存（保留给未来"叶子目录跳过"优化使用）
		dirNorm := strings.ReplaceAll(dir, "\\", "/")
		if dInfo, err := os.Stat(dir); err == nil {
			a.mu.Lock()
			a.recordDirMtime(dirNorm, dInfo.ModTime().UnixMilli())
			a.mu.Unlock()
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

			// 已存在于 DB → 跳过（不重复读尺寸）
			if oldIDs[id] {
				unchanged++
				continue
			}

			// 新文件 → 创建条目（尺寸由后台懒回填）
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
				IsVideo:      isVideoFile(item.Name()),
			})
		}
	}
	// ★ 支持刷新子文件夹：rel 从该文件夹相对其根目录的路径开始，
	//   否则新条目的 folder 字段会丢失"根→该文件夹"的路径段。
	matchedRootNorm := strings.ReplaceAll(matchedRoot, "\\", "/")
	relBase := ""
	if normalizedFolder != matchedRootNorm {
		relBase = normalizedFolder[len(matchedRootNorm)+1:]
	}
	walkFn(folderPath, relBase)

	// ★ 持久化目录 mtime 缓存
	a.saveDirMtimes()

	// 1c. 计算删除的 ID：oldIDs 中不在 currentIDs 里的（含 DB 孤儿）
	var removedIDs []string
	for id := range oldIDs {
		if !currentIDs[id] {
			removedIDs = append(removedIDs, id)
		}
	}

	if len(addedEntries) == 0 && len(removedIDs) == 0 {
		// ★ 即使没有增删，也校准一次该根目录计数：可能上次导入/落库被并发扫描干扰，
		//   folders 计数停留在偏小/0 值（表现为"点刷新计数一直不变"）。
		if a.imageDB != nil {
			a.imageDB.RecomputeFolderCountsForRoot(matchedRoot, nil)
		}
		return &FolderDiffResult{Added: []SafeImage{}, Removed: []string{}, Unchanged: unchanged, Success: true}
	}

	// === Phase 2: SQLite 持久化（ON CONFLICT 保证幂等）===

	if a.imageDB != nil {
		if len(removedIDs) > 0 {
			if err := a.imageDB.DeleteImagesBatch(removedIDs); err != nil {
				fmt.Printf("[增量刷新] 搜索索引删除失败: %v\n", err)
			}
			if err := a.imageDB.DeleteImageCacheBatch(removedIDs); err != nil {
				fmt.Printf("[增量刷新] image_cache 删除失败: %v\n", err)
			}
		}
		if len(addedEntries) > 0 {
			// ★ 分批写入（每批 ~2000 条）：批间释放 ImageDB 单一互斥锁，
			//   前台浏览请求可插队，避免"刷新后迟迟不出图"。
			entries := make([]database.ImageCacheEntry, 0, len(addedEntries))
			for _, entry := range addedEntries {
				entries = append(entries, database.ImageCacheEntry{
					ID: entry.ID, Path: entry.Path, Name: entry.Name, Size: entry.Size,
					LastModified: entry.LastModified, CreatedAt: entry.CreatedAt,
					Folder: entry.Folder, RootPath: entry.RootPath,
					IsVideo: entry.IsVideo,
				})
			}
			const insertBatchSize = 2000
			for i := 0; i < len(entries); i += insertBatchSize {
				end := i + insertBatchSize
				if end > len(entries) {
					end = len(entries)
				}
				if err := a.imageDB.SaveImageCacheBatchCounted(matchedRoot, entries[i:end]); err != nil {
					fmt.Printf("[增量刷新] image_cache 批量写入失败 (%d 条): %v\n", end-i, err)
					break
				}
			}
		}
		// === Phase 3: 幂等校准 folders 计数（吸收删除带来的减量）===
		a.imageDB.RecomputeFolderCountsForRoot(matchedRoot, nil)
	}

	// ★ 数据提交后才失效预览缓存：无变化的刷新（提前 return）不失效，
	//   避免每次右键刷新后 GetFolders 对全部预览文件夹重查（COUNT+OFFSET 拖慢树刷新）
	a.invalidatePreviewCache()
	// 数据变更：生成参数标签缓存一并失效
	a.invalidateParamTags()

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
// 根目录，但 image_cache 没有它的记录（历史 bug 误删、中断扫描残留等）。
// 后台对该文件夹做一次增量刷新（diff → 直接写 DB）；有变化时发 scan:complete
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
