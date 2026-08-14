package main

import (
	"fmt"
	"strings"
)

// isContentDuplicate 检查文件内容是否已在 image_cache 中存在。
// 仅在 DB 中有相同 size 的记录时才计算 SHA256（避免全量 I/O）。
func (a *App) isContentDuplicate(fullPath string, fileSize int64) (bool, string, error) {
	if a.imageDB == nil {
		return false, "", nil
	}
	count, err := a.imageDB.CountBySizeForDedup(fileSize)
	if err != nil || count == 0 {
		return false, "", err
	}
	hash := computeFileSHA256(fullPath)
	if hash == "" {
		return false, "", nil
	}
	exists, err := a.imageDB.ExistsByContentHash(hash, fileSize)
	return exists, hash, err
}

// BackfillContentHashes 分批回填 image_cache 中 content_hash 为空的记录。
// 每批 100 条，间隔 100ms，避免影响主线程。
func (a *App) BackfillContentHashes() {
	if a.imageDB == nil {
		return
	}
	const batchSize = 100
	for {
		entries, err := a.imageDB.GetNullContentHashBatch(batchSize)
		if err != nil {
			fmt.Printf("[去重] 查询空 content_hash 失败: %v\n", err)
			return
		}
		if len(entries) == 0 {
			fmt.Printf("[去重] content_hash 回填完成\n")
			return
		}
		for _, e := range entries {
			hash := computeFileSHA256(e.Path)
			if hash == "" {
				continue // 文件可能已不存在
			}
			if err := a.imageDB.UpdateContentHash(e.ID, hash); err != nil {
				fmt.Printf("[去重] 回填 content_hash 失败 %s: %v\n", e.ID, err)
			}
		}
	}
}

// CleanDuplicateImages 清理内容重复的图片（按 content_hash 分组，每组只保留一条）。
// 返回清理数量。前端可以通过 Wails API 调用此方法。
func (a *App) CleanDuplicateImages() map[string]interface{} {
	if a.imageDB == nil {
		return map[string]interface{}{"success": false, "error": "数据库未初始化"}
	}

	// 1. 确保所有条目都有 content_hash
	a.BackfillContentHashes()

	// 2. 找出重复组
	dups, err := a.imageDB.LoadDuplicatesByContentHash()
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}

	totalRemoved := 0
	var allRemovedIDs []string

	for hash, ids := range dups {
		_ = hash
		// ids[0] 是保留的（最早导入），ids[1:] 是重复的
		removed := ids
		allRemovedIDs = append(allRemovedIDs, removed...)
		totalRemoved += len(removed)

		// 从内存中删除
		a.mu.Lock()
		for _, id := range removed {
			delete(a.images, id)
		}
		// 从 folderIndex 中移除
		for folderKey, fids := range a.folderIndex {
			var remaining []string
			removedSet := make(map[string]bool, len(removed))
			for _, id := range removed {
				removedSet[id] = true
			}
			for _, id := range fids {
				if !removedSet[id] {
					remaining = append(remaining, id)
				}
			}
			if len(remaining) == 0 {
				a.folderIndex[folderKey] = nil
			} else {
				a.folderIndex[folderKey] = remaining
			}
		}
		a.mu.Unlock()
	}

	if totalRemoved == 0 {
		return map[string]interface{}{"success": true, "cleaned": 0, "message": "没有发现重复图片"}
	}

	// 3. 从 SQLite 删除
	if err := a.imageDB.DeleteImagesBatch(allRemovedIDs); err != nil {
		fmt.Printf("[去重] SQLite 批量删除失败: %v\n", err)
	}
	if err := a.imageDB.DeleteImageCacheBatch(allRemovedIDs); err != nil {
		fmt.Printf("[去重] image_cache 批量删除失败: %v\n", err)
	}

	// 4. 清理缩略图缓存
	a.removeThumbsByIDs(allRemovedIDs)

	// 5. 重建 folderCount
	a.mu.Lock()
	a.rebuildFolderCounts()
	a.mu.Unlock()

	fmt.Printf("[去重] 清理完成：共移除 %d 张重复图片\n", totalRemoved)
	return map[string]interface{}{"success": true, "cleaned": totalRemoved}
}

// dedupAfterScan 扫描后的去重处理：回填 content_hash 并清理重复。
// 只处理指定 rootPath 的条目，轻量执行。
func (a *App) dedupAfterScan(rootPath string) {
	if a.imageDB == nil {
		return
	}
	normalizedRoot := strings.ReplaceAll(rootPath, "\\", "/")

	// 只回填该 root 下 content_hash 为空的条目
	const batchSize = 100
	for {
		entries, err := a.imageDB.GetNullContentHashBatch(batchSize)
		if err != nil {
			return
		}
		if len(entries) == 0 {
			break
		}
		for _, e := range entries {
			// 只处理属于该 root 的条目
			eRoot := strings.ReplaceAll(e.RootPath, "\\", "/")
			if eRoot != normalizedRoot && !strings.HasPrefix(eRoot, normalizedRoot+"/") {
				continue
			}
			hash := computeFileSHA256(e.Path)
			if hash == "" {
				continue
			}
			if err := a.imageDB.UpdateContentHash(e.ID, hash); err != nil {
				fmt.Printf("[去重] 回填 content_hash 失败 %s: %v\n", e.ID, err)
			}
		}
	}

	// 回填完成后检查该 root 下是否有重复
	dups, err := a.imageDB.LoadDuplicatesByContentHash()
	if err != nil {
		return
	}
	var rootRemovedIDs []string
	for _, ids := range dups {
		if len(ids) <= 1 {
			continue
		}
		// 检查重复组是否属于当前 root
		for _, id := range ids[1:] {
			if entry, err := a.imageDB.GetImageEntry(id); err == nil && entry != nil {
				eRoot := strings.ReplaceAll(entry.RootPath, "\\", "/")
				if eRoot == normalizedRoot || strings.HasPrefix(eRoot, normalizedRoot+"/") {
					rootRemovedIDs = append(rootRemovedIDs, id)
				}
			}
		}
	}
	if len(rootRemovedIDs) == 0 {
		return
	}

	// 从内存中删除
	a.mu.Lock()
	removedSet := make(map[string]bool, len(rootRemovedIDs))
	for _, id := range rootRemovedIDs {
		delete(a.images, id)
		removedSet[id] = true
	}
	for folderKey, fids := range a.folderIndex {
		var remaining []string
		for _, id := range fids {
			if !removedSet[id] {
				remaining = append(remaining, id)
			}
		}
		a.folderIndex[folderKey] = remaining
	}
	a.rebuildFolderCounts()
	a.mu.Unlock()

	// 从 SQLite 删除
	a.imageDB.DeleteImageCacheBatch(rootRemovedIDs)
	// 清理缩略图
	a.removeThumbsByIDs(rootRemovedIDs)

	fmt.Printf("[去重] %s: 移除 %d 张重复图片\n", rootPath, len(rootRemovedIDs))
}