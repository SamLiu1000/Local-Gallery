package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
			Code:    "keyword_empty",
		}
	}

	if a.imageDB == nil {
		return &SearchResponse{
			Success: false,
			Message: "图片元数据库未初始化，搜索功能不可用",
			Code:    "db_not_init",
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
			Code:    "search_failed",
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
			Code:    "db_not_init",
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
			Code:    "no_conditions",
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
			Code:    "advanced_failed",
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

// paramTagCategoryDefs 生成参数标签面板的类别白名单（展示顺序 = 数组顺序）。
// key 是 params_json 中的参数键；field 是点击标签时直接用于高级搜索的条件字段
// （"json:Key" 前缀由 getFields 识别为从 params_json 提取对应键）。
// mode 是点击标签时的匹配模式：名称类（Model/LoRA/VAE 等）用 contains，
// 数值类（CFG/Steps/Size）用 exact 避免 "7" 误命中 "7.5"。
// 不在白名单里的键（如 Seed、内部噪声键）不展示，避免标签爆炸。
var paramTagCategoryDefs = []struct {
	Key   string
	Field string
	Mode  string
}{
	{"Model", "json:Model", "contains"},
	{"LoRA", "json:LoRA", "contains"},
	{"Sampler", "json:Sampler", "contains"},
	{"Scheduler", "json:Scheduler", "contains"},
	{"Schedule type", "json:Schedule type", "contains"},
	{"CFG Scale", "json:CFG Scale", "exact"},
	{"Distilled CFG Scale", "json:Distilled CFG Scale", "exact"},
	{"Steps", "json:Steps", "exact"},
	{"Size", "json:Size", "exact"},
	{"VAE", "json:VAE", "contains"},
	{"CLIP", "json:CLIP", "contains"},
	{"Clip Skip", "json:Clip Skip", "exact"},
	{"Denoising strength", "json:Denoising strength", "exact"},
	{"Hires upscaler", "json:Hires upscaler", "contains"},
	{"Hires upscale", "json:Hires upscale", "exact"},
	{"Hires steps", "json:Hires steps", "exact"},
}

// maxTagsPerGroup 每个类别最多展示的标签数（按出现次数降序取前 N）
const maxTagsPerGroup = 200

// splitLoRATags 把 params_json 里的 LoRA 值（如 "name1:0.80, name2:1.00"）拆成
// 单个 LoRA 名称标签（去掉权重后缀），并按名称聚合出现次数。
// 这样标签面板展示的是"单个 LoRA 名"，而不是整串组合值。
func splitLoRATags(values map[string]int) map[string]int {
	out := make(map[string]int)
	for v, c := range values {
		for _, part := range strings.Split(v, ",") {
			name := strings.TrimSpace(part)
			// 去掉 ":权重" 后缀（兼容 "name:0.80" / "name:0.8" 格式）
			if idx := strings.LastIndex(name, ":"); idx > 0 {
				name = strings.TrimSpace(name[:idx])
			}
			if name == "" {
				continue
			}
			out[name] += c
		}
	}
	return out
}

// GetParamTags 返回生成参数标签面板数据：按参数类别分组，汇总每类的取值分布。
// forceRefresh=true 时绕过缓存强制重新聚合（面板上"刷新"按钮使用）。
// 结果按 类别白名单顺序 排列，每类标签按出现次数降序，最多 maxTagsPerGroup 个。
//
// ★ 持久化：聚合结果写入 userDataDir/paramtags-cache.json，重启后直接读取磁盘缓存，
//   不再每次打开面板都做全库 json_each 聚合（50 万行约 4 秒）。只有
//   invalidateParamTags（导入新文件夹 / 右键刷新 / 元数据回填完成）会清空内存+磁盘缓存，
//   下次打开才重新计算。
func (a *App) GetParamTags(forceRefresh bool) *ParamTagsResponse {
	if a.imageDB == nil {
		return &ParamTagsResponse{Success: false, Message: "图片元数据库未初始化", Code: "db_not_init"}
	}

	a.paramTagsMu.Lock()
	defer a.paramTagsMu.Unlock()

	// 内存缓存命中（非强制刷新）
	if !forceRefresh && a.paramTagsCache != nil {
		return &ParamTagsResponse{Success: true, Groups: a.paramTagsCache}
	}

	// 尝试从磁盘读取上次持久化的结果（重启后首次打开也能秒开）
	if !forceRefresh {
		if groups, ok := a.loadParamTagsFromDisk(); ok {
			a.paramTagsCache = groups
			a.paramTagsCacheAt = time.Now()
			return &ParamTagsResponse{Success: true, Groups: groups}
		}
	}

	// 重新聚合
	agg, err := a.imageDB.GetParamTagAggregation()
	if err != nil {
		fmt.Printf("[参数标签] 聚合失败: %v\n", err)
		return &ParamTagsResponse{Success: false, Message: "生成参数聚合失败: " + err.Error(), Code: "agg_failed"}
	}

	groups := make([]ParamTagGroup, 0, len(paramTagCategoryDefs))
	for _, def := range paramTagCategoryDefs {
		values := agg[def.Key]
		if len(values) == 0 {
			continue
		}
		if def.Key == "LoRA" {
			values = splitLoRATags(values)
		}
		tags := make([]ParamTagItem, 0, len(values))
		for v, c := range values {
			tags = append(tags, ParamTagItem{Value: v, Count: c})
		}
		// 按出现次数降序
		sort.Slice(tags, func(i, j int) bool {
			if tags[i].Count != tags[j].Count {
				return tags[i].Count > tags[j].Count
			}
			return tags[i].Value < tags[j].Value
		})
		if len(tags) > maxTagsPerGroup {
			tags = tags[:maxTagsPerGroup]
		}
		groups = append(groups, ParamTagGroup{Key: def.Key, Field: def.Field, Mode: def.Mode, Tags: tags})
	}

	a.paramTagsCache = groups
	a.paramTagsCacheAt = time.Now()
	// 持久化到磁盘，供下次启动直接读取
	if err := a.saveParamTagsToDisk(groups); err != nil {
		fmt.Printf("[参数标签] 持久化缓存失败: %v\n", err)
	}
	return &ParamTagsResponse{Success: true, Groups: groups}
}

// paramTagsCachePath 参数标签磁盘缓存文件路径（位于 userDataDir）
func (a *App) paramTagsCachePath() string {
	return filepath.Join(a.userDataDir, "paramtags-cache.json")
}

// saveParamTagsToDisk 将聚合结果写入磁盘缓存（先写临时文件再改名，避免写入中断损坏）
func (a *App) saveParamTagsToDisk(groups []ParamTagGroup) error {
	data, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	tmp := a.paramTagsCachePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, a.paramTagsCachePath())
}

// loadParamTagsFromDisk 读取磁盘缓存；文件缺失/损坏时返回 false
func (a *App) loadParamTagsFromDisk() ([]ParamTagGroup, bool) {
	data, err := os.ReadFile(a.paramTagsCachePath())
	if err != nil {
		return nil, false
	}
	var groups []ParamTagGroup
	if err := json.Unmarshal(data, &groups); err != nil {
		fmt.Printf("[参数标签] 磁盘缓存解析失败，忽略: %v\n", err)
		return nil, false
	}
	if len(groups) == 0 {
		return nil, false
	}
	return groups, true
}

// invalidateParamTags 使参数标签缓存失效（扫描/回填等数据变更后调用）。
// 同时删除磁盘缓存，确保下次打开面板重新聚合而不是读到过期数据。
func (a *App) invalidateParamTags() {
	a.paramTagsMu.Lock()
	a.paramTagsCache = nil
	os.Remove(a.paramTagsCachePath())
	a.paramTagsMu.Unlock()
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

// backfillImageMetadata 后台回填搜索索引缺失的元数据（Model/LoRA/CFG 等生成参数）。
// 旧扫描/增量刷新写入 images 表时只填了 id/path，params_json 为空，导致按参数搜索不到。
// 多 worker 并发读取文件头部提取并 upsert；无元数据的文件标记为 {}（合法 JSON），
// 避免下次启动重复解析。分批 + 节流，不阻塞界面，可自然续跑。
func (a *App) backfillImageMetadata() {
	if a.imageDB == nil {
		return
	}
	const batchSize = 400
	const workers = 8
	jobs := make(chan database.ImageCacheEntry, workers*2)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range jobs {
				if _, err := os.Stat(r.Path); err != nil {
					// 文件已删除：标记 {} 避免重复
					a.imageDB.MarkMetadataEmpty(r.ID)
					continue
				}
				// 有元数据 → 提取并 upsert；无元数据 → indexImageMetadata 也会写 {} 占位
				a.indexImageMetadata(r.ID, r.Path, r.Name, r.Size,
					r.LastModified, r.CreatedAt, r.Folder, r.RootPath)
			}
		}()
	}
	processed := 0
	for {
		rows, err := a.imageDB.GetImagesNeedingMetadataBackfill(batchSize)
		if err != nil {
			fmt.Printf("[元数据回填] 查询失败: %v\n", err)
			break
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			jobs <- r
		}
		processed += len(rows)
		if processed%20000 == 0 {
			fmt.Printf("[元数据回填] 进度: %d 条\n", processed)
		}
	}
	close(jobs)
	wg.Wait()
	fmt.Printf("[元数据回填] 完成，处理 %d 条\n", processed)
	// ★ 只有确实处理了数据才失效参数标签缓存。
	//   否则每次启动即使回填 0 条也会删掉磁盘缓存 → 用户每次点"生成参数"都要重新聚合（慢）。
	if processed > 0 {
		a.invalidateParamTags()
	}
}

// repairSearchIndex 修复搜索索引：将内存中有但数据库中缺失的图片元数据补写回 SQLite
// 在应用启动时异步调用，确保之前扫描的图片也能被搜索到
//
// 注意：轻量索引加载后 a.images 仅含 LRU 缓存中的部分图片，此函数不再可靠。
// 现已改为按根目录 ensureRootLoaded 后再遍历。main.go 已注释调用，保留函数备用。
func (a *App) repairSearchIndex() {
	if a.imageDB == nil {
		return
	}

	a.mu.RLock()
	totalRoots := len(a.registeredRoots)
	rootsCopy := make([]string, 0, totalRoots)
	for root := range a.registeredRoots {
		rootsCopy = append(rootsCopy, root)
	}
	a.mu.RUnlock()

	if totalRoots == 0 {
		return
	}

	// 对每个根目录：ensureRootLoaded 后再检查索引
	for _, rootPath := range rootsCopy {
		a.ensureRootLoaded(rootPath)
	}

	// 快速路径：如果数据库记录数与 image_cache 一致，跳过
	a.mu.RLock()
	totalImages := len(a.images)
	a.mu.RUnlock()
	if totalImages == 0 {
		return
	}
	if stats := a.imageDB.GetStats(); stats != nil {
		if dbCount, ok := stats["totalImages"].(int); ok && dbCount >= totalImages {
			fmt.Printf("[索引修复] 搜索索引完整（DB: %d, 内存: %d），跳过检查\n", dbCount, totalImages)
			return
		}
	}

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

		// photo 类型根目录跳过
		a.mu.RLock()
		ft := a.folderTypes[rootPath]
		a.mu.RUnlock()
		if ft == "photo" {
			fmt.Printf("[索引] %s 是 photo 类型，跳过\n", rootPath)
			return
		}

		// ★ 直接从 SQL 读取该根目录所有图片元数据，避免 LRU 淘汰漏数据
		entries, err := a.imageDB.LoadImageCacheByRoot(rootPath)
		if err != nil {
			fmt.Printf("[索引] %s 读取 image_cache 失败: %v\n", rootPath, err)
			return
		}
		total := len(entries)
		if total == 0 {
			fmt.Printf("[索引] %s 无图片需索引\n", rootPath)
			return
		}

		for i, e := range entries {
			select {
			case <-ctx.Done():
				fmt.Printf("[索引] %s 已取消 (进度 %d/%d)\n", rootPath, i, total)
				return
			default:
			}

			if _, err := os.Stat(e.Path); err != nil {
				continue // 文件已删除
			}
			a.indexImageMetadata(e.ID, e.Path, e.Name,
				e.Size, e.LastModified, e.CreatedAt,
				e.Folder, e.RootPath)

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
