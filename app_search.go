package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"local-gallery/internal/database"
)

// SearchImages 在图片元数据库中搜索
// query: 搜索关键词（在 path/prompt/negative_prompt/params_json 中模糊匹配）
// folder: 可选，限定搜索范围
// offset: 分页偏移
// limit: 每页条数
func (a *App) SearchImages(query string, folder string, offset int, limit int) *SearchResponse {
	if query == "" {
		return &SearchResponse{
			Success: false,
			Message: "搜索关键词不能为空",
		}
	}

	if a.imageDB == nil {
		return &SearchResponse{
			Success: false,
			Message: "图片元数据库未初始化，搜索功能不可用",
		}
	}

	if limit <= 0 || limit > 10000 {
		limit = 1000
	}

	results, total, err := a.imageDB.SearchImagesBySubstring(query, folder, offset, limit)
	if err != nil {
		return &SearchResponse{
			Success: false,
			Message: fmt.Sprintf("搜索失败: %v", err),
		}
	}

	// 确保 items 不为 nil（JSON 序列化时 nil 和 [] 不同）
	items := results
	if items == nil {
		items = []database.SearchResult{}
	}

	return &SearchResponse{
		Success: true,
		Items:   items,
		Total:   total,
		Offset:  offset,
		Limit:   limit,
	}
}

// AdvancedSearch 高级搜索：支持多条件、多文件夹、日期范围、多种匹配模式
func (a *App) AdvancedSearch(request *AdvancedSearchRequest) *AdvancedSearchResponse {
	if a.imageDB == nil {
		return &AdvancedSearchResponse{
			Success: false,
			Message: "图片元数据库未初始化，搜索功能不可用",
		}
	}

	if request.Limit <= 0 || request.Limit > 10000 {
		request.Limit = 1000
	}

	// 验证：至少有一个条件或文件夹过滤或日期范围
	hasConditions := len(request.Conditions) > 0
	hasFolders := len(request.Folders) > 0
	hasDateRange := request.DateFrom > 0 || request.DateTo > 0
	if !hasConditions && !hasFolders && !hasDateRange {
		return &AdvancedSearchResponse{
			Success: false,
			Message: "请至少指定一个搜索条件、文件夹过滤或日期范围",
		}
	}

	// 验证并规范化匹配模式
	matchMode := request.MatchMode
	if matchMode != "and" && matchMode != "or" {
		matchMode = "or"
	}

	fmt.Printf("[高级搜索] 收到 %d 个条件: %+v\n", len(request.Conditions), request.Conditions)
	// 转换条件
	var dbConditions []database.SearchCondition
	for _, c := range request.Conditions {
		if c.Value == "" {
			continue
		}
		mode := c.Mode
		if mode != "exact" && mode != "exclude" && mode != "word" {
			mode = "contains"
		}
		dbConditions = append(dbConditions, database.SearchCondition{
			Field: c.Field,
			Value: c.Value,
			Mode:  mode,
		})
	}

	results, total, err := a.imageDB.SearchImagesAdvanced(dbConditions, request.Folders, request.DateFrom, request.DateTo, matchMode, request.Offset, request.Limit)
	if err != nil {
		return &AdvancedSearchResponse{
			Success: false,
			Message: fmt.Sprintf("高级搜索失败: %v", err),
		}
	}

	items := results
	if items == nil {
		items = []database.SearchResult{}
	}

	return &AdvancedSearchResponse{
		Success: true,
		Items:   items,
		Total:   total,
		Offset:  request.Offset,
		Limit:   request.Limit,
	}
}

// indexImageMetadata 扫描后索引单张图片的元数据到 SQLite
// 在 scanDirectoryToMap 中被调用
func (a *App) indexImageMetadata(id, path, name string, size, lastModified, createdAt int64, folder, rootPath string) {
	if a.imageDB == nil {
		return
	}

	// 快速解析元数据（只支持 PNG，对于搜索已经足够覆盖主要用例）
	meta := a.ParseMetadataFast(path)
	prompt := ""
	negativePrompt := ""
	paramsJSON := "{}"
	rawJSON := "{}"

	if meta != nil {
		if p, ok := meta["prompt"].(string); ok {
			prompt = p
		}
		if np, ok := meta["negativePrompt"].(string); ok {
			negativePrompt = np
		}
		if raw, ok := meta["raw"]; ok {
			if rawBytes, err := json.Marshal(raw); err == nil {
				rawJSON = string(rawBytes)
			}
		}
		if params, ok := meta["params"]; ok {
			if paramsBytes, err := json.Marshal(params); err == nil {
				paramsJSON = string(paramsBytes)
			}
		}
	}

	record := &database.ImageRecord{
		ID:             id,
		Path:           path,
		Name:           name,
		Size:           size,
		LastModified:   lastModified,
		CreatedAt:      createdAt,
		Folder:         folder,
		RootPath:       rootPath,
		Prompt:         prompt,
		NegativePrompt: negativePrompt,
		ParamsJSON:     paramsJSON,
		RawJSON:        rawJSON,
	}

	if err := a.imageDB.IndexImage(record); err != nil {
		// 静默失败 - 索引失败不应中断扫描流程
		fmt.Printf("[索引] 写入数据库失败 [%s]: %v\n", name, err)
	}
}

// batchIndexImages 批量索引图片元数据（后台调用，不阻塞扫描完成通知）
// folderType: "ai"=全部索引, "mixed"=仅索引有元数据的文件, "photo"=不调用
func (a *App) batchIndexImages(images map[string]*ImageEntry, folderType string) {
	if a.imageDB == nil || len(images) == 0 || folderType == "photo" {
		return
	}
	var records []*database.ImageRecord
	for id, entry := range images {
		meta := a.ParseMetadataFast(entry.Path)
		prompt := ""
		negativePrompt := ""
		paramsJSON := "{}"
		rawJSON := "{}"
		if meta != nil {
			if p, ok := meta["prompt"].(string); ok {
				prompt = p
			}
			if np, ok := meta["negativePrompt"].(string); ok {
				negativePrompt = np
			}
			if raw, ok := meta["raw"]; ok {
				if rawBytes, err := json.Marshal(raw); err == nil {
					rawJSON = string(rawBytes)
				}
			}
			if params, ok := meta["params"]; ok {
				if paramsBytes, err := json.Marshal(params); err == nil {
					paramsJSON = string(paramsBytes)
				}
			}
		}
		// mixed 模式：无元数据则跳过，不写入数据库
		if folderType == "mixed" && meta == nil {
			continue
		}
		records = append(records, &database.ImageRecord{
			ID:             id,
			Path:           entry.Path,
			Name:           entry.Name,
			Size:           entry.Size,
			LastModified:   entry.LastModified,
			Folder:         entry.Folder,
			RootPath:       entry.RootPath,
			Prompt:         prompt,
			NegativePrompt: negativePrompt,
			ParamsJSON:     paramsJSON,
			RawJSON:        rawJSON,
		})
	}
	indexed, err := a.imageDB.IndexBatch(records)
	if err != nil {
		fmt.Printf("[批量索引] 失败: %v\n", err)
	} else if indexed > 0 {
		fmt.Printf("[批量索引] 已索引 %d 张图片元数据\n", indexed)
	}
}

// repairSearchIndex 修复搜索索引：将内存中有但数据库中缺失的图片元数据补写回 SQLite
// 在应用启动时异步调用，确保之前扫描的图片也能被搜索到
func (a *App) repairSearchIndex() {
	if a.imageDB == nil {
		return
	}

	a.mu.RLock()
	totalImages := len(a.images)
	a.mu.RUnlock()

	if totalImages == 0 {
		return
	}

	// 快速路径：如果数据库记录数与内存图片数一致，说明全部已索引，直接跳过
	if stats := a.imageDB.GetStats(); stats != nil {
		if dbCount, ok := stats["totalImages"].(int); ok && dbCount >= totalImages {
			fmt.Printf("[索引修复] 搜索索引完整（DB: %d, 内存: %d），跳过检查\n", dbCount, totalImages)
			return
		}
	}

	// 详细修复：找出数据库中缺失的图片并补索引
	existingIDs, err := a.imageDB.GetExistingIDs()
	if err != nil {
		fmt.Printf("[索引修复] 获取已有 ID 失败: %v\n", err)
		return
	}

	a.mu.RLock()
	var toIndex []*ImageEntry
	for id, entry := range a.images {
		if !existingIDs[id] {
			if ft, ok := a.folderTypes[entry.RootPath]; ok && ft == "photo" {
				continue
			}
			toIndex = append(toIndex, entry)
		}
	}
	a.mu.RUnlock()

	if len(toIndex) == 0 {
		fmt.Printf("[索引修复] 搜索索引完整，%d 张图片全部已索引\n", totalImages)
		return
	}

	fmt.Printf("[索引修复] 发现 %d 张图片缺失搜索索引（共 %d 张），开始后台补建...\n", len(toIndex), totalImages)

	indexed := 0
	for _, entry := range toIndex {
		a.indexImageMetadata(entry.ID, entry.Path, entry.Name, entry.Size, entry.LastModified, entry.CreatedAt, entry.Folder, entry.RootPath)
		indexed++
		if indexed%500 == 0 {
			fmt.Printf("[索引修复] 进度: %d / %d\n", indexed, len(toIndex))
		}
	}

	fmt.Printf("[索引修复] 完成！已补建 %d 张图片的搜索索引\n", indexed)
}

// IndexRootInfo 单个根目录的索引状态
type IndexRootInfo struct {
	RootPath  string `json:"rootPath"`
	Total     int    `json:"total"`
	Indexed   int    `json:"indexed"`
	Done      bool   `json:"done"`
	Indexing  bool   `json:"indexing"`
}

// GetFolderIndexStatus 返回所有已注册根目录的索引状态
func (a *App) GetFolderIndexStatus() []IndexRootInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.imageDB == nil {
		return nil
	}

	var result []IndexRootInfo
	for rootPath := range a.registeredRoots {
		rootNorm := strings.ReplaceAll(rootPath, "\\", "/")
		total := a.folderCount[rootNorm]
		indexed := a.imageDB.CountByRoot(rootPath)
		a.indexingRootsMu.Lock()
		_, isIndexing := a.indexingRoots[rootPath]
		a.indexingRootsMu.Unlock()
		result = append(result, IndexRootInfo{
			RootPath: rootPath,
			Total:    total,
			Indexed:  indexed,
			Done:     total > 0 && indexed >= total,
			Indexing: isIndexing,
		})
	}
	return result
}

// IndexRoot 对指定根目录启动后台搜索索引构建
func (a *App) IndexRoot(rootPath string) {
	if a.imageDB == nil {
		return
	}

	// 防止重复启动
	a.indexingRootsMu.Lock()
	if _, ok := a.indexingRoots[rootPath]; ok {
		a.indexingRootsMu.Unlock()
		fmt.Printf("[索引] %s 已在索引中，跳过\n", rootPath)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.indexingRoots[rootPath] = cancel
	a.indexingRootsMu.Unlock()

	fmt.Printf("[索引] 开始索引: %s\n", rootPath)

	go func() {
		defer func() {
			a.indexingRootsMu.Lock()
			delete(a.indexingRoots, rootPath)
			a.indexingRootsMu.Unlock()
			cancel()
		}()

		// 收集该根目录下的所有图片
		a.mu.RLock()
		var toIndex []*ImageEntry
		rootNorm := strings.ReplaceAll(rootPath, "\\", "/")
		for folderKey, ids := range a.folderIndex {
			if folderKey == rootNorm || strings.HasPrefix(folderKey, rootNorm+"/") {
				for _, id := range ids {
					if entry, ok := a.images[id]; ok {
						if ft, ok2 := a.folderTypes[entry.RootPath]; ok2 && ft == "photo" {
							continue
						}
						toIndex = append(toIndex, entry)
					}
				}
			}
		}
		total := len(toIndex)
		a.mu.RUnlock()

		if total == 0 {
			fmt.Printf("[索引] %s 无图片需索引\n", rootPath)
			return
		}

		for i, entry := range toIndex {
			select {
			case <-ctx.Done():
				fmt.Printf("[索引] %s 已取消 (进度 %d/%d)\n", rootPath, i, total)
				return
			default:
			}

			if _, err := os.Stat(entry.Path); err != nil {
				continue // 文件已删除
			}
			a.indexImageMetadata(entry.ID, entry.Path, entry.Name,
				entry.Size, entry.LastModified, entry.CreatedAt,
				entry.Folder, entry.RootPath)

			if (i+1)%500 == 0 {
				fmt.Printf("[索引] %s 进度: %d / %d\n", rootPath, i+1, total)
			}
		}

		fmt.Printf("[索引] %s 完成: %d 张\n", rootPath, total)
	}()
}

// StopIndexRoot 停止指定根目录的索引
func (a *App) StopIndexRoot(rootPath string) {
	a.indexingRootsMu.Lock()
	cancel, ok := a.indexingRoots[rootPath]
	if ok {
		cancel()
		delete(a.indexingRoots, rootPath)
	}
	a.indexingRootsMu.Unlock()
}
