package main

import (
	"container/list"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

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
	roots := make([]string, 0, len(a.registeredRoots))
	for r := range a.registeredRoots {
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
			var wg sync.WaitGroup
			for _, rootPath := range missingRoots {
				wg.Add(1)
				go func(rp string) {
					defer wg.Done()
					a.scanRootAsync(rp)
				}(rootPath)
			}
			wg.Wait()
			return 0 // 增量扫描是异步的，不返回总数
		}
		fmt.Printf("[扫描] 所有根目录已有数据，跳过扫描\n")
		return 0
	}

	if len(roots) == 0 {
		fmt.Printf("[扫描] 没有已注册的根目录，跳过扫描\n")
		return 0
	}

	type scanResult struct {
		count       int
		images      map[string]*ImageEntry
		folderIndex map[string][]string
	}
	resultCh := make(chan scanResult, len(roots))
	var wg sync.WaitGroup

	for _, rootPath := range roots {
		normalizedPath, err := filepath.Abs(rootPath)
		if err != nil {
			continue
		}
		info, err := os.Stat(normalizedPath)
		if err != nil || !info.IsDir() {
			continue
		}
		wg.Add(1)
		go func(rp string) {
			defer wg.Done()
			localImages := make(map[string]*ImageEntry)
			localFolderIndex := make(map[string][]string)
			count := a.scanDirectoryToMap(rp, rp, localImages, localFolderIndex)
			resultCh <- scanResult{count: count, images: localImages, folderIndex: localFolderIndex}
		}(normalizedPath)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	tempImages := make(map[string]*ImageEntry)
	tempFolderIndex := make(map[string][]string)
	totalCount := 0
	for result := range resultCh {
		totalCount += result.count
		for k, v := range result.images {
			tempImages[k] = v
		}
		for k, v := range result.folderIndex {
			tempFolderIndex[k] = append(tempFolderIndex[k], v...)
		}
	}

	a.mu.Lock()
	// ★ 与 LRU 协同：清空旧 LRU 状态，写入扫描结果并标记所有 folderKey 为 loaded
	a.images = tempImages
	a.folderIndex = tempFolderIndex
	a.folderLoaded = make(map[string]bool)
	if a.folderLRU == nil {
		a.folderLRU = list.New()
	} else {
		a.folderLRU.Init()
	}
	a.lruNodes = make(map[string]*list.Element)
	for fk := range tempFolderIndex {
		a.folderLoaded[fk] = true
		a.lruNodes[fk] = a.folderLRU.PushBack(fk)
	}
	a.rebuildFolderCounts()
	a.mu.Unlock()

	a.saveImageIndex()
	fmt.Printf("[扫描] 全部完成: 共 %d 张图片，%d 个根目录\n", totalCount, len(roots))
	// 通知前端扫描完成
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
			"rootPath": "",
			"count":    totalCount,
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

	// 读取 EXIF Orientation 标签，如果图片需要旋转 90° 或 270°，交换宽高
	if orientation := readEXIFOrientation(filePath); orientation >= 5 && orientation <= 8 {
		w, h = h, w
	}
	return w, h
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
		if !isVideo {
			w, h = getImageDimensions(fullPath)
		}
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
		if !isVideo {
			w, h = getImageDimensions(fullPath)
		}
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

func (a *App) scanDirectory(dirPath, rootPath string) int {
	res := a.scanWalk(rootPath, nil, nil, true)
	return res.count
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

func (a *App) scanDirectoryToMap(dirPath, rootPath string, images map[string]*ImageEntry, folderIndex map[string][]string) int {
	res := a.scanWalk(rootPath, images, folderIndex, false)
	return res.count
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
	if a.folderCount == nil { a.folderCount = make(map[string]int) }
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

// buildFolderTreeFromIndex 从 folderIndex 内存构建文件夹树，零磁盘 I/O
// 比 buildFolderTreeRecursive（os.ReadDir）快几个数量级
func (a *App) buildFolderTreeFromIndex(rootPath string, thumbCounts map[string]int) []*FolderNode {
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
			nodes[subPath] = &FolderNode{
				Name:       parts[i],
				Path:       filepath.Join(rootPath, filepath.Join(parts[:i+1]...)),
				ImageCount: a.folderCount[subPath],
				ThumbCount: thumbCounts[subPath],
				Children:   nil,
			}
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

func (a *App) scanRootAsync(rootPath string) {
	if a.bgPaused.Load() == 1 { return }
	a.scanMu.Lock()
	if a.scanningRoots[rootPath] { a.scanMu.Unlock(); return }
	a.scanningRoots[rootPath] = true
	a.scanMu.Unlock()
	defer func() { a.scanMu.Lock(); delete(a.scanningRoots, rootPath); a.scanMu.Unlock() }()
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
		if !a.registeredRoots[rootPath] { a.mu.Unlock(); return }
		for k, v := range batchImages { a.images[k] = v; localImages[k] = v }
		for k, v := range batchFolderIndex { a.folderIndex[k] = append(a.folderIndex[k], v...); localFolderIndex[k] = append(localFolderIndex[k], v...) }
		a.incrementFolderCounts(batchImages)
		a.mu.Unlock()

		totalCount += batchCount
		if a.ctx != nil && batchCount > 0 {
			wailsruntime.EventsEmit(a.ctx, "scan:batch", map[string]interface{}{
				"rootPath":   rootPath,
				"folder":     folderRel,
				"count":      batchCount,
				"totalSoFar": totalCount,
			})
		}
	})

	a.mu.Lock()
	if !a.registeredRoots[rootPath] { a.mu.Unlock(); fmt.Printf("[后台扫描] 根目录已被移除，丢弃扫描结果: %s\n", rootPath); return }
	a.removeByRootFromMemory(rootPath)
	for k, v := range localImages { a.images[k] = v }
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
	a.evictLRU()
	a.mu.Unlock()
	a.saveImageIndexForRoot(rootPath)
	a.mu.Lock()
	a.rebuildFolderCountsFromSQLLocked()
	a.mu.Unlock()

	// 再次确保 folderCount 正确后再通知完成
	a.mu.Lock()
	folderCount := a.folderCount[strings.ReplaceAll(rootPath, "\\", "/")]
	a.mu.Unlock()
	fmt.Printf("[后台扫描] 验证: rootPath=%s, folderCount=%d, folderIndexKeys=%d\n", rootPath, folderCount, len(localFolderIndex))

	ft := a.folderTypes[rootPath]
	if ft == "" { ft = "ai" }
	if ft != "photo" { go a.batchIndexImages(localImages, ft) }
	fmt.Printf("[后台扫描] 完成: %s，共 %d ���图片\n", rootPath, totalCount)
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
			"rootPath": rootPath, "count": totalCount,
		})
	}
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
			fmt.Printf("[启动修复] folderIndex 完整（%d 个文件夹，%d 个根目录），跳过检查\n", totalFolders, totalRoots)
			return
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
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			deadRoots = append(deadRoots, root)
			continue
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
			wailsruntime.EventsEmit(a.ctx, "scan:complete", map[string]interface{}{
				"rootPath": root,
				"count":    len(result.Added) + result.Unchanged,
				"added":    len(result.Added),
				"removed":  len(result.Removed),
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
// 返回新增和删除数量，前端可根据结果决定是否重载页面
func (a *App) RefreshAll() map[string]interface{} {
	fmt.Println("[RefreshAll] 前端触发全量刷新...")
	a.ensureImageIndex()
	a.startupIncrementalRefresh()
	// a.repairSearchIndex()
	fmt.Println("[RefreshAll] 全量刷新完成")
	return map[string]interface{}{"success": true}
}

// ==================== 增量刷新 ====================

// refreshFolderInternal 对指定文件夹做增量对比，只处理新增和删除的图片。
// folderPath 是文件系统路径（如 D:\AIImages\sub）。
//
// 锁策略：
//   Phase 1 — IO（Walk + getImageDimensions），无锁
//   Phase 2 — SQLite 写入（a.imageDB）
//   Phase 3 — 内存更新（a.mu.Lock）
//   Phase 4 — JSON 持久化（a.saveImageIndex）
//
// stableID 基于 filePath+fileSize+fileModified 的 MD5。
// 已知限制：文件被移动/重命名会判定为删除+新增，丢失已有 metadata。
func (a *App) refreshFolderInternal(folderPath string) *FolderDiffResult {
	normalizedFolder := strings.ReplaceAll(folderPath, "\\", "/")

	// 找到包含此文件夹的已注册根目录
	var matchedRoot string
	a.mu.RLock()
	for root := range a.registeredRoots {
		rootNorm := strings.ReplaceAll(root, "\\", "/")
		if normalizedFolder == rootNorm || strings.HasPrefix(normalizedFolder, rootNorm+"/") {
			matchedRoot = root
			break
		}
	}
	a.mu.RUnlock()
	if matchedRoot == "" {
		return &FolderDiffResult{Success: false, Error: "未找到匹配的已注册根目录: " + folderPath}
	}

	// 检查文件夹是否存在
	info, err := os.Stat(folderPath)
	if err != nil || !info.IsDir() {
		return &FolderDiffResult{Success: false, Error: "文件夹不存在: " + folderPath}
	}

	// === Phase 1: IO 阶段（无锁）===

	// 1a. 快照：拷贝 folderIndex 中属于此文件夹树的 ID 集合（短暂读锁）
	a.mu.RLock()
	oldIDs := make(map[string]bool)
	for folderKey, ids := range a.folderIndex {
		if folderKey == normalizedFolder || strings.HasPrefix(folderKey, normalizedFolder+"/") {
			for _, id := range ids {
				oldIDs[id] = true
			}
		}
	}
	a.mu.RUnlock()

	// 1b. Walk 文件夹，对每个图片文件计算 stableID 并做对比
	currentIDs := make(map[string]bool)
	var addedEntries []*ImageEntry
	unchanged := 0

	_ = filepath.Walk(folderPath, func(filePath string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过无法访问的文件
		}
		if fi.IsDir() {
			return nil
		}
		if !isImageFile(filePath) && !isVideoFile(filePath) {
			return nil
		}
		id := generateStableID(filePath, fi.Size(), fi.ModTime().UnixMilli())
		currentIDs[id] = true

		// 已存在 → 跳过
		a.mu.RLock()
		_, exists := a.images[id]
		a.mu.RUnlock()
		if exists {
			unchanged++
			return nil
		}

		// 新文件 → 获取尺寸并创建条目
		isVideo := isVideoFile(fi.Name())
		w, h := 0, 0
		if !isVideo {
			w, h = getImageDimensions(filePath)
		}
		relFolder, _ := filepath.Rel(matchedRoot, filepath.Dir(filePath))
		relFolder = strings.ReplaceAll(relFolder, "\\", "/")
		if relFolder == "." {
			relFolder = ""
		}

		entry := &ImageEntry{
			ID:           id,
			Path:         filePath,
			Name:         fi.Name(),
			Size:         fi.Size(),
			LastModified: fi.ModTime().UnixMilli(),
			CreatedAt:    getFileCreationTimeMillis(filePath, fi),
			Folder:       relFolder,
			RootPath:     matchedRoot,
			URL:          fmt.Sprintf("/image/%s", id),
			Width:        w,
			Height:       h,
			IsVideo:      isVideo,
		}
		addedEntries = append(addedEntries, entry)
		return nil
	})

	// 1c. 计算删除的 ID：oldIDs 中不在 currentIDs 里的
	var removedIDs []string
	for id := range oldIDs {
		if !currentIDs[id] {
			removedIDs = append(removedIDs, id)
		}
	}

	if len(addedEntries) == 0 && len(removedIDs) == 0 {
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
			for _, entry := range addedEntries {
				record := &database.ImageRecord{
					ID:           entry.ID,
					Path:         entry.Path,
					Name:         entry.Name,
					Size:         entry.Size,
					LastModified: entry.LastModified,
					CreatedAt:    entry.CreatedAt,
					Folder:       entry.Folder,
					RootPath:     entry.RootPath,
				}
				if err := a.imageDB.IndexImage(record); err != nil {
					fmt.Printf("[增量刷新] SQLite 插入失败 %s: %v\n", entry.Path, err)
				}
			}
		}
	}

	// SQLite 写入成功后内存更新失败（极罕见）：
	// 持久化已完成，下次启动会自动恢复一致状态。
	// 调用方收到 error 可安全重试，SQL 操作（ON CONFLICT / DELETE）保证幂等。

	// === Phase 3: 内存更新 ===

	a.mu.Lock()
	for _, id := range removedIDs {
		delete(a.images, id)
	}
	for _, entry := range addedEntries {
		a.images[entry.ID] = entry
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
			} else {
				a.folderIndex[folderKey] = remaining
			}
		}
	}
	// 将新增条目加入 folderIndex
	for _, entry := range addedEntries {
		rootNorm := strings.ReplaceAll(entry.RootPath, "\\", "/")
		folderKey := rootNorm
		if entry.Folder != "" {
			folderKey = rootNorm + "/" + strings.ReplaceAll(entry.Folder, "\\", "/")
		}
		a.folderIndex[folderKey] = append(a.folderIndex[folderKey], entry.ID)
		affectedKeys[folderKey] = true
	}
	// ★ 与 LRU 协同：增量刷新结果即权威数据，标记受影响 folderKey 为 loaded
	for fk := range affectedKeys {
		if !a.folderLoaded[fk] {
			a.folderLoaded[fk] = true
			a.lruNodes[fk] = a.folderLRU.PushBack(fk)
		} else {
			a.touchFolderLocked(fk)
		}
	}
	a.evictLRU()
	a.mu.Unlock()

	// === Phase 4: JSON 持久化 ===
	a.saveImageIndexForRoot(folderPath)
	a.mu.Lock()
	a.rebuildFolderCountsFromSQLLocked()
	a.mu.Unlock()

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
