package main

import (
	"container/list"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"local-gallery/internal/database"
)

// ==================== 持久化操作 ====================

func (a *App) loadUserData() {
	fmt.Printf("[加载] 从 %s 加载用户数据\n", a.userDataFile)

	// 优先从 SQLite 加载导入根目录
	if a.userDataDB != nil {
		roots, err := a.userDataDB.GetAllRoots()
		if err != nil {
			fmt.Printf("[加载] 从 SQLite 读取导入根目录失败: %v，回退到 JSON\n", err)
		} else if len(roots) > 0 {
			a.mu.Lock()
			for _, r := range roots {
				if info, err := os.Stat(r.Path); err == nil && info.IsDir() {
					a.registeredRoots[r.Path] = true
					if r.FolderType != "" {
						a.folderTypes[r.Path] = r.FolderType
					}
				} else {
					fmt.Printf("[加载] 目录不存在，跳过: %s\n", r.Path)
					a.userDataDB.RemoveRoot(r.Path)
				}
			}
			a.mu.Unlock()
			fmt.Printf("[加载] 从 SQLite 注册 %d 个根目录\n", len(a.registeredRoots))
			return
		}
		// SQLite 表为空（len(roots)==0）：继续尝试 JSON fallback 以支持旧数据迁移
	}

	// fallback: 从 JSON 加载
	data := a.readUserDataFile()
	a.mu.Lock()
	if roots, ok := data["registeredRoots"].([]interface{}); ok {
		var validRoots []string
		for _, r := range roots {
			if path, ok := r.(string); ok {
				if info, err := os.Stat(path); err == nil && info.IsDir() {
					a.registeredRoots[path] = true
					validRoots = append(validRoots, path)
				} else {
					fmt.Printf("[加载] 目录不存在，跳过: %s\n", path)
				}
			}
		}
		// 将 JSON 中的根目录迁移到 SQLite
		if a.userDataDB != nil && len(validRoots) > 0 {
			now := time.Now().Format(time.RFC3339)
			var iroots []database.ImportedRoot
			for _, r := range validRoots {
				ft := a.folderTypes[r]
				iroots = append(iroots, database.ImportedRoot{
					Path: r, Name: filepath.Base(r), FolderType: ft, AddedAt: now,
				})
			}
			if err := a.userDataDB.SaveRoots(iroots); err == nil {
				fmt.Printf("[加载] 已将 %d 个根目录从 JSON 迁移到 SQLite\n", len(validRoots))
			}
		}
		if len(validRoots) < len(roots) {
			data["registeredRoots"] = validRoots
			a.writeUserDataFile(data)
			fmt.Printf("[加载] 已清理 %d 个不存在的目录\n", len(roots)-len(validRoots))
		}
		// 从 JSON 中删除 registeredRoots，后续完全由 SQLite 管理
		if len(validRoots) > 0 {
			delete(data, "registeredRoots")
			delete(data, "folderTypes")
			a.writeUserDataFile(data)
			fmt.Printf("[加载] 已从 JSON 中移除 registeredRoots，后续完全由 SQLite 管理\n")
		}
	}
	a.mu.Unlock()
	fmt.Printf("[加载] 已注册 %d 个根目录\n", len(a.registeredRoots))
}

func (a *App) loadImageIndex() {
	if a.imageDB == nil {
		return
	}
	entries, err := a.imageDB.LoadAllImageCache()
	if err != nil {
		fmt.Printf("[缓存] 从 SQLite 加载图片索引失败: %v\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Printf("[缓存] 图片索引缓存为空，等待扫描填充\n")
		return
	}
	a.mu.Lock()
	a.images = make(map[string]*ImageEntry, len(entries))
	a.folderIndex = make(map[string][]string)
	a.folderCount = make(map[string]int)
	for _, e := range entries {
		rootNorm := strings.ReplaceAll(e.RootPath, "\\", "/")
		// 修复：如果 Folder 是完整路径，需要转换为相对路径
		folderRel := e.Folder
		if folderRel != "" {
			folderNorm := strings.ReplaceAll(folderRel, "\\", "/")
			// 如果 Folder 以 rootNorm 开头，说明是完整路径，需要截取相对部分
			if strings.HasPrefix(folderNorm, rootNorm+"/") {
				folderRel = folderNorm[len(rootNorm)+1:]
			} else if folderNorm == rootNorm {
				folderRel = ""
			}
		}
		a.images[e.ID] = &ImageEntry{
			ID: e.ID, Path: e.Path, Name: e.Name, Size: e.Size,
			LastModified: e.LastModified, CreatedAt: e.CreatedAt,
			Folder: folderRel, RootPath: e.RootPath,
			Width: e.Width, Height: e.Height, IsVideo: e.IsVideo,
			URL: fmt.Sprintf("/image/%s", e.ID),
		}
		fk := rootNorm
		if folderRel != "" {
			fk = rootNorm + "/" + folderRel
		}
		a.folderIndex[fk] = append(a.folderIndex[fk], e.ID)
		// 统计根目录和子文件夹的数量
		a.folderCount[rootNorm]++
		if fk != rootNorm {
			a.folderCount[fk]++
		}
	}
	a.mu.Unlock()
	a.rebuildFolderCounts()
	// 自修复：检测并修复重复路径的 folderIndex 键（如 "K:/bid/K:\bid\..."）
	a.fixDuplicatePathKeys()
	fmt.Printf("[缓存] 已从 SQLite 加载图片索引: %d 张图片，%d 个文件夹\n", len(entries), len(a.folderIndex))
}

// ==================== 轻量索引 + LRU 按需加载 ====================

const (
	maxLoadedFolders = 32    // LRU 容量：按文件夹数
	maxLoadedImages  = 50000 // LRU 硬上限：防单文件夹巨量
)

// normalizeFolderRel 将 image_cache.Folder 字段规范化为相对 root 的子路径。
// 处理两种历史格式：完整路径（以 root 开头）和相对路径。
func normalizeFolderRel(folder, rootNorm string) string {
	if folder == "" {
		return ""
	}
	folderNorm := strings.ReplaceAll(folder, "\\", "/")
	if strings.HasPrefix(folderNorm, rootNorm+"/") {
		return folderNorm[len(rootNorm)+1:]
	}
	if folderNorm == rootNorm {
		return ""
	}
	return folderNorm
}

// toImageEntry 将 ImageCacheEntry 转为 ImageEntry（复用 loadImageIndex 内的构造）。
func toImageEntry(e database.ImageCacheEntry) *ImageEntry {
	rootNorm := strings.ReplaceAll(e.RootPath, "\\", "/")
	folderRel := normalizeFolderRel(e.Folder, rootNorm)
	return &ImageEntry{
		ID: e.ID, Path: e.Path, Name: e.Name, Size: e.Size,
		LastModified: e.LastModified, CreatedAt: e.CreatedAt,
		Folder: folderRel, RootPath: e.RootPath,
		Width: e.Width, Height: e.Height, IsVideo: e.IsVideo,
		URL: fmt.Sprintf("/image/%s", e.ID),
	}
}

// splitFolderKey 将规范化 folderKey 拆分为 (rootPath, folderRel)。
// folderKey 形如 "K:/photos/sub"，返回 ("K:/photos", "sub")；folderKey 即 root 时返回 (rootPath, "")。
// 需遍历 registeredRoots 做最长前缀匹配，因 rootPath 本身可能含 "/"。
func (a *App) splitFolderKey(folderKey string) (rootPath, folderRel string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var bestRootNorm, bestRoot string
	for root := range a.registeredRoots {
		rootNorm := strings.ReplaceAll(root, "\\", "/")
		if folderKey == rootNorm {
			return root, ""
		}
		if strings.HasPrefix(folderKey, rootNorm+"/") {
			if len(rootNorm) > len(bestRootNorm) {
				bestRootNorm = rootNorm
				bestRoot = root
			}
		}
	}
	if bestRoot != "" {
		return bestRoot, folderKey[len(bestRootNorm)+1:]
	}
	return folderKey, ""
}

// loadFolderIndexLight 仅加载文件夹列表与计数，不读图片详情。启动时用，替代 loadImageIndex。
func (a *App) loadFolderIndexLight() {
	if a.imageDB == nil {
		return
	}
	entries, err := a.imageDB.LoadFolderIndexLight()
	if err != nil {
		fmt.Printf("[缓存] 轻量索引加载失败 %v，回退全量\n", err)
		a.loadImageIndex()
		return
	}
	if len(entries) == 0 {
		fmt.Printf("[缓存] 轻量索引为空，等待扫描填充\n")
		return
	}
	a.mu.Lock()
	a.images = make(map[string]*ImageEntry)     // 启动时空
	a.folderIndex = make(map[string][]string)   // key 存在，value=nil 标记"未加载"
	a.folderLoaded = make(map[string]bool)
	a.folderCount = make(map[string]int)
	if a.folderLRU == nil {
		a.folderLRU = list.New()
	}
	if a.lruNodes == nil {
		a.lruNodes = make(map[string]*list.Element)
	}
	a.folderLRU.Init()
	a.lruNodes = make(map[string]*list.Element)
	for _, e := range entries {
		rootNorm := strings.ReplaceAll(e.RootPath, "\\", "/")
		folderRel := normalizeFolderRel(e.Folder, rootNorm)
		fk := rootNorm
		if folderRel != "" {
			fk = rootNorm + "/" + folderRel
		}
		a.folderIndex[fk] = nil // 标记未加载
		count := int(e.Size)
		a.folderCount[rootNorm] += count
		if folderRel != "" {
			parts := strings.Split(folderRel, "/")
			for i := 1; i <= len(parts); i++ {
				sub := rootNorm + "/" + strings.Join(parts[:i], "/")
				a.folderCount[sub] += count
			}
		}
	}
	a.mu.Unlock()
	a.fixDuplicatePathKeys()
	fmt.Printf("[缓存] 轻量索引就绪: %d 个文件夹\n", len(a.folderIndex))
}

// rebuildFolderCountsFromSQL 从 SQL 重建 folderCount，覆盖所有根目录。
// 用于扫描后或增量刷新后准确地刷新计数（a.images 是部分 LRU，不可信）。
// 调用方需持写锁 a.mu.Lock()。
func (a *App) rebuildFolderCountsFromSQLLocked() {
	if a.imageDB == nil {
		return
	}
	entries, err := a.imageDB.LoadFolderIndexLight()
	if err != nil {
		fmt.Printf("[计数] 从 SQL 重建 folderCount 失败: %v\n", err)
		return
	}
	counts := make(map[string]int)
	for _, e := range entries {
		rootNorm := strings.ReplaceAll(e.RootPath, "\\", "/")
		folderRel := normalizeFolderRel(e.Folder, rootNorm)
		count := int(e.Size)
		counts[rootNorm] += count
		if folderRel != "" {
			parts := strings.Split(folderRel, "/")
			for i := 1; i <= len(parts); i++ {
				sub := rootNorm + "/" + strings.Join(parts[:i], "/")
				counts[sub] += count
			}
		}
	}
	a.folderCount = counts
}

// ensureFolderLoaded 同步加载某 folderKey 进缓存；带 double-check + LRU 淘汰。
func (a *App) ensureFolderLoaded(folderKey string) {
	a.mu.RLock()
	if a.folderLoaded[folderKey] {
		a.touchFolderLocked(folderKey)
		a.mu.RUnlock()
		return
	}
	a.mu.RUnlock()

	root, folderRel := a.splitFolderKey(folderKey)
	entries, err := a.imageDB.LoadImageCacheByFolder(root, folderRel)
	if err != nil || len(entries) == 0 {
		// 即使无图片也标记 loaded，避免重复查询空文件夹
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.folderLoaded[folderKey] {
			return
		}
		a.folderLoaded[folderKey] = true
		if a.folderIndex[folderKey] == nil {
			a.folderIndex[folderKey] = []string{}
		}
		a.lruNodes[folderKey] = a.folderLRU.PushBack(folderKey)
		a.evictLRU()
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.folderLoaded[folderKey] { // double-check
		return
	}
	if a.folderIndex[folderKey] == nil {
		a.folderIndex[folderKey] = make([]string, 0, len(entries))
	}
	for _, e := range entries {
		entry := toImageEntry(e)
		a.images[e.ID] = entry
		a.folderIndex[folderKey] = append(a.folderIndex[folderKey], e.ID)
	}
	a.folderLoaded[folderKey] = true
	a.lruNodes[folderKey] = a.folderLRU.PushBack(folderKey)
	a.evictLRU()
}

// ensureRootLoaded 加载整个根目录所有子文件夹（全量遍历回退用）。
func (a *App) ensureRootLoaded(rootPath string) {
	rootNorm := strings.ReplaceAll(rootPath, "\\", "/")
	// 先检查是否所有子 folderKey 都已 loaded
	a.mu.RLock()
	allLoaded := true
	for folderKey := range a.folderIndex {
		if folderKey == rootNorm || strings.HasPrefix(folderKey, rootNorm+"/") {
			if !a.folderLoaded[folderKey] {
				allLoaded = false
				break
			}
		}
	}
	a.mu.RUnlock()
	if allLoaded {
		return
	}

	entries, err := a.imageDB.LoadImageCacheByRoot(rootPath)
	if err != nil {
		return
	}
	// 按 folderKey 分组
	grouped := make(map[string][]database.ImageCacheEntry)
	for _, e := range entries {
		rootN := strings.ReplaceAll(e.RootPath, "\\", "/")
		folderRel := normalizeFolderRel(e.Folder, rootN)
		fk := rootN
		if folderRel != "" {
			fk = rootN + "/" + folderRel
		}
		grouped[fk] = append(grouped[fk], e)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for fk, groupEntries := range grouped {
		if a.folderLoaded[fk] {
			continue
		}
		if a.folderIndex[fk] == nil {
			a.folderIndex[fk] = make([]string, 0, len(groupEntries))
		}
		for _, e := range groupEntries {
			entry := toImageEntry(e)
			a.images[e.ID] = entry
			a.folderIndex[fk] = append(a.folderIndex[fk], e.ID)
		}
		a.folderLoaded[fk] = true
		a.lruNodes[fk] = a.folderLRU.PushBack(fk)
	}
	a.evictLRU()
}

// touchFolderLocked 更新 LRU 顺序（命中时调用，需持锁）。
func (a *App) touchFolderLocked(folderKey string) {
	if elem, ok := a.lruNodes[folderKey]; ok {
		a.folderLRU.MoveToBack(elem)
	}
}

// countLoadedImagesLocked 统计当前已加载的图片总数（需持锁）。
func (a *App) countLoadedImagesLocked() int {
	count := 0
	for _, ids := range a.folderIndex {
		if len(ids) > 0 {
			count += len(ids)
		}
	}
	return count
}

// evictLRU 淘汰最久未用的文件夹（需持写锁）。
func (a *App) evictLRU() {
	for len(a.folderLoaded) > maxLoadedFolders || a.countLoadedImagesLocked() > maxLoadedImages {
		elem := a.folderLRU.Front()
		if elem == nil {
			return
		}
		oldKey := elem.Value.(string)
		a.folderLRU.Remove(elem)
		delete(a.lruNodes, oldKey)
		for _, id := range a.folderIndex[oldKey] {
			delete(a.images, id)
		}
		a.folderIndex[oldKey] = nil // 保留 key 标记"未加载"
		delete(a.folderLoaded, oldKey)
	}
}

// invalidateFolder 使某 folderKey 失效（扫描刷新时调用）。
func (a *App) invalidateFolder(folderKey string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.folderLoaded[folderKey] {
		return
	}
	for _, id := range a.folderIndex[folderKey] {
		delete(a.images, id)
	}
	a.folderIndex[folderKey] = nil
	delete(a.folderLoaded, folderKey)
	if elem, ok := a.lruNodes[folderKey]; ok {
		a.folderLRU.Remove(elem)
		delete(a.lruNodes, folderKey)
	}
}

// invalidateAllFolders 清空所有 LRU 缓存（scanAllFolders 原子替换前调用）。
func (a *App) invalidateAllFolders() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.images = make(map[string]*ImageEntry)
	a.folderLoaded = make(map[string]bool)
	if a.folderLRU == nil {
		a.folderLRU = list.New()
	} else {
		a.folderLRU.Init()
	}
	a.lruNodes = make(map[string]*list.Element)
}

// markFolderLoadedLocked 标记某 folderKey 已加载（扫描写入后调用，需持写锁）。
// 同时更新 LRU。
func (a *App) markFolderLoadedLocked(folderKey string, ids []string) {
	if a.folderIndex[folderKey] == nil {
		a.folderIndex[folderKey] = ids
	} else {
		// 已有 ID 列表，合并去重
		existing := make(map[string]bool, len(a.folderIndex[folderKey]))
		for _, id := range a.folderIndex[folderKey] {
			existing[id] = true
		}
		for _, id := range ids {
			if !existing[id] {
				a.folderIndex[folderKey] = append(a.folderIndex[folderKey], id)
				existing[id] = true
			}
		}
	}
	if !a.folderLoaded[folderKey] {
		a.folderLoaded[folderKey] = true
		a.lruNodes[folderKey] = a.folderLRU.PushBack(folderKey)
		a.evictLRU()
	} else {
		a.touchFolderLocked(folderKey)
	}
}

func (a *App) saveImageIndex() {
	if a.imageDB != nil {
		go a.saveImageIndexToSQLite()
	}
}

func (a *App) saveImageIndexForRoot(rootPath string) {
	if a.imageDB == nil {
		return
	}
	normalized := strings.ReplaceAll(rootPath, "\\", "/")
	go a.saveImageIndexByRoot(normalized)
}

func (a *App) saveImageIndexByRoot(rootPath string) {
	a.mu.RLock()
	// Collect all image IDs under this root from folderIndex
	imageIDs := make(map[string]bool)
	for folderKey, ids := range a.folderIndex {
		if folderKey == rootPath || strings.HasPrefix(folderKey, rootPath+"/") {
			for _, id := range ids {
				imageIDs[id] = true
			}
		}
	}
	// Build entries only for images belonging to this root
	entries := make([]database.ImageCacheEntry, 0, len(imageIDs))
	for id := range imageIDs {
		if img, ok := a.images[id]; ok {
			entries = append(entries, database.ImageCacheEntry{
				ID: img.ID, Path: img.Path, Name: img.Name, Size: img.Size,
				LastModified: img.LastModified, CreatedAt: img.CreatedAt,
				Folder: img.Folder, RootPath: img.RootPath,
				Width: img.Width, Height: img.Height, IsVideo: img.IsVideo,
			})
		}
	}
	a.mu.RUnlock()
	if len(entries) == 0 {
		fmt.Printf("[增量存储] %s: 无图片数据，跳过\n", rootPath)
		return
	}
	if err := a.imageDB.SaveImageCacheByRoot(rootPath, entries); err != nil {
		fmt.Printf("[增量存储] %s: 写入失败: %v\n", rootPath, err)
	} else {
		fmt.Printf("[增量存储] %s: 已保存 %d 张图片\n", rootPath, len(entries))
	}
}

func (a *App) saveImageIndexToSQLite() {
	a.mu.RLock()
	const batchSize = 2000
	entries := make([]database.ImageCacheEntry, 0, batchSize)
	for _, img := range a.images {
		entries = append(entries, database.ImageCacheEntry{
			ID: img.ID, Path: img.Path, Name: img.Name, Size: img.Size,
			LastModified: img.LastModified, CreatedAt: img.CreatedAt,
			Folder: img.Folder, RootPath: img.RootPath,
			Width: img.Width, Height: img.Height, IsVideo: img.IsVideo,
		})
		if len(entries) >= batchSize {
			a.imageDB.SaveImageCacheBatch(entries)
			entries = entries[:0]
		}
	}
	a.mu.RUnlock()
	if len(entries) > 0 {
		a.imageDB.SaveImageCacheBatch(entries)
	}
}

func (a *App) saveRegisteredRoots() {
	if a.userDataDB == nil {
		return
	}
	a.mu.RLock()
	var importedRoots []database.ImportedRoot
	now := time.Now().Format(time.RFC3339)
	for r := range a.registeredRoots {
		ft := a.folderTypes[r]
		name := filepath.Base(r)
		ir := database.ImportedRoot{
			Path:       r,
			Name:       name,
			FolderType: ft,
			AddedAt:    now,
		}
		importedRoots = append(importedRoots, ir)
	}
	a.mu.RUnlock()

	// 合并现有元数据（display_name, handle_name 等前端维护的字段）
	if err := a.userDataDB.MergeRoots(importedRoots); err != nil {
		fmt.Printf("[错误] 保存注册目录到 SQLite 失败: %v\n", err)
	}
}

// migrateUserData 一次性迁移：从 user-data.json 迁移数据到 SQLite
func (a *App) migrateUserData() {
	if a.userDataDB == nil {
		return
	}
	rootCount, err := a.userDataDB.GetRootCount()
	if err != nil || rootCount > 0 {
		return // 已经迁移过或出错
	}

	data := a.readUserDataFile()
	now := time.Now().Format(time.RFC3339)
	migrated := false

	// 迁移 registeredRoots + folderTypes
	if roots, ok := data["registeredRoots"].([]interface{}); ok && len(roots) > 0 {
		ftMap, _ := data["folderTypes"].(map[string]interface{})
		var iroots []database.ImportedRoot
		for _, r := range roots {
			if path, ok := r.(string); ok {
				ft := ""
				if ftMap != nil {
					if v, ok := ftMap[path].(string); ok {
						ft = v
					}
				}
				iroots = append(iroots, database.ImportedRoot{
					Path:       path,
					Name:       filepath.Base(path),
					FolderType: ft,
					AddedAt:    now,
				})
			}
		}
		if err := a.userDataDB.SaveRoots(iroots); err == nil {
			delete(data, "registeredRoots")
			delete(data, "folderTypes")
			migrated = true
			fmt.Printf("[迁移] 已迁移 %d 个导入目录到 SQLite\n", len(iroots))
		}
	}

	// 迁移 settings.importedRoots → 更新 display_name
	if settings, ok := data["settings"].(map[string]interface{}); ok {
		if impRoots, ok := settings["importedRoots"].([]interface{}); ok {
			for _, ir := range impRoots {
				if rm, ok := ir.(map[string]interface{}); ok {
					rootID, _ := rm["rootId"].(string)
					displayName, _ := rm["displayName"].(string)
					handleName, _ := rm["handleName"].(string)
					if rootID != "" && (displayName != "" || handleName != "") {
						a.userDataDB.UpdateRootMeta(rootID, displayName, handleName)
					}
				}
			}
			delete(settings, "importedRoots")
			data["settings"] = settings
			migrated = true
			fmt.Printf("[迁移] 已更新导入目录元数据\n")
		}
	}

	// 迁移 imageTags
	if imgTags, ok := data["imageTags"].([]interface{}); ok && len(imgTags) > 0 {
		var tags []database.ImageTag
		for _, it := range imgTags {
			if itm, ok := it.(map[string]interface{}); ok {
				imagePath, _ := itm["imagePath"].(string)
				tagID, _ := itm["tagId"].(string)
				addedAt, _ := itm["addedAt"].(string)
				if imagePath != "" && tagID != "" {
					if addedAt == "" {
						addedAt = now
					}
					tags = append(tags, database.ImageTag{ImagePath: imagePath, TagID: tagID, AddedAt: addedAt})
				}
			}
		}
		if len(tags) > 0 {
			if err := a.userDataDB.ImportImageTags(tags); err == nil {
				delete(data, "imageTags")
				migrated = true
				fmt.Printf("[迁移] 已迁移 %d 个图片标签到 SQLite\n", len(tags))
			}
		}
	}

	// 迁移 favorites
	if favs, ok := data["favorites"].([]interface{}); ok && len(favs) > 0 {
		var paths []string
		for _, f := range favs {
			switch v := f.(type) {
			case string:
				paths = append(paths, v)
			case map[string]interface{}:
				if p, ok := v["imagePath"].(string); ok {
					paths = append(paths, p)
				}
			}
		}
		if len(paths) > 0 {
			if err := a.userDataDB.ImportFavorites(paths, now); err == nil {
				delete(data, "favorites")
				migrated = true
				fmt.Printf("[迁移] 已迁移 %d 个收藏到 SQLite\n", len(paths))
			}
		}
	}

	if migrated {
		// 写回清理后的 user-data.json（只保留 settings/apiConfigs/tags）
		if err := a.writeUserDataFile(data); err != nil {
			fmt.Printf("[迁移] 清理 user-data.json 失败: %v\n", err)
		} else {
			fmt.Printf("[迁移] 已清理 user-data.json 中的已迁移数据\n")
		}
	}
}

func (a *App) readUserDataFile() map[string]interface{} {
	data := make(map[string]interface{})
	bytes, err := os.ReadFile(a.userDataFile)
	if err != nil {
		return data
	}
	json.Unmarshal(bytes, &data)
	return data
}

func (a *App) writeUserDataFile(data map[string]interface{}) error {
	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON 序列化失败: %w", err)
	}
	if err := os.WriteFile(a.userDataFile, bytes, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	return nil
}

// AppendReverseLog 追加一条反推日志记录到 user 目录下的日志文件
func (a *App) AppendReverseLog(name string, path string, errMsg string) {
	logFile := filepath.Join(a.userDataDir, "reverse-log.txt")
	dir := filepath.Dir(logFile)
	os.MkdirAll(dir, 0755)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	status := "成功"
	if errMsg != "" {
		status = fmt.Sprintf("失败: %s", errMsg)
	}
	line := fmt.Sprintf("[%s] %s | %s | %s\n", timestamp, status, name, path)
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("[反推日志] 写入失败: %v\n", err)
		return
	}
	defer f.Close()
	f.WriteString(line)
}

// GetReverseLog 读取反推日志文件内容
func (a *App) GetReverseLog() string {
	logFile := filepath.Join(a.userDataDir, "reverse-log.txt")
	data, err := os.ReadFile(logFile)
	if err != nil {
		return ""
	}
	return string(data)
}

// OpenReverseLog 在文件管理器中打开反推日志文件
func (a *App) OpenReverseLog() error {
	logFile := filepath.Join(a.userDataDir, "reverse-log.txt")
	dir := filepath.Dir(logFile)
	os.MkdirAll(dir, 0755)
	// 确保文件存在
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		os.WriteFile(logFile, []byte{}, 0644)
	}
	return exec.Command("explorer", "/select,", logFile).Start()
}

// fixDuplicatePathKeys 修复 folderIndex 中重复路径的键（如 "K:/bid/K:\bid\..."）
func (a *App) fixDuplicatePathKeys() {
	fixedCount := 0
	newIndex := make(map[string][]string)

	for folderKey, ids := range a.folderIndex {
		// 规范化键：统一使用正斜杠
		normalizedKey := strings.ReplaceAll(folderKey, "\\", "/")

		// 检测并修复重复路径模式（如 "K:/bid/K:/bid/..."）
		// 方法：找到驱动器号+第一个路径部分，如果整个键以这个模式开头两次，则删除重复
		parts := strings.Split(normalizedKey, "/")
		if len(parts) >= 4 {
			// 检查是否有驱动器号 + 路径部分被重复
			// 例如 ["K:", "bid", "K:", "bid", "ATKArchives"]
			// 找到第二次出现驱动器号的位置
			firstDrive := parts[0] // e.g., "K:"
			var secondDriveIdx = -1
			for i := 1; i < len(parts)-1; i++ {
				if parts[i] == firstDrive && i+1 < len(parts) && parts[i+1] == parts[1] {
					secondDriveIdx = i
					break
				}
			}
			if secondDriveIdx > 0 {
				// 找到重复，删除从开头到重复开始的所有部分
				correctedKey := strings.Join(parts[secondDriveIdx:], "/")
				fixedCount++
				fmt.Printf("[自修复] folderIndex: %q -> %q\n", folderKey, correctedKey)
				normalizedKey = correctedKey
			}
		}

		newIndex[normalizedKey] = append(newIndex[normalizedKey], ids...)
	}

	if fixedCount > 0 {
		a.mu.Lock()
		a.folderIndex = newIndex

		// 同时修复 folderCount 中的重复路径键
		newFolderCount := make(map[string]int)
		for folderKey, count := range a.folderCount {
			normalizedKey := strings.ReplaceAll(folderKey, "\\", "/")
			parts := strings.Split(normalizedKey, "/")
			if len(parts) >= 4 {
				firstDrive := parts[0]
				var secondDriveIdx = -1
				for i := 1; i < len(parts)-1; i++ {
					if parts[i] == firstDrive && i+1 < len(parts) && parts[i+1] == parts[1] {
						secondDriveIdx = i
						break
					}
				}
				if secondDriveIdx > 0 {
					correctedKey := strings.Join(parts[secondDriveIdx:], "/")
					normalizedKey = correctedKey
				}
			}
			newFolderCount[normalizedKey] = count
		}
		a.folderCount = newFolderCount

		a.mu.Unlock()
		fmt.Printf("[自修复] 已修复 %d 个重复路径的 folderIndex 键，并同步修复 folderCount\n", fixedCount)
	}
}
