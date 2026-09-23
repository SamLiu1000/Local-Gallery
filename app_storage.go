package main

import (
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
// ★ 改造（M2/M3）：磁盘扫描 → SQLite；所有 UI 读取纯 SQL。
//   原内存索引（a.images / folderIndex / folderCount / LRU）已删除，
//   图片索引与文件夹计数全部以 image_cache / folders 表为唯一真相源。

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
			rootList := make([]string, 0, len(a.registeredRoots))
			for root := range a.registeredRoots {
				rootList = append(rootList, root)
			}
			fmt.Printf("[删除诊断] 启动时 registeredRoots=%v\n", rootList)
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
	rootList := make([]string, 0, len(a.registeredRoots))
	for root := range a.registeredRoots {
		rootList = append(rootList, root)
	}
	fmt.Printf("[删除诊断] 启动时 registeredRoots=%v\n", rootList)
}

// ==================== 条目/键的工具函数 ====================

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

// toImageEntry 将 ImageCacheEntry 转为 ImageEntry。
func toImageEntry(e database.ImageCacheEntry) *ImageEntry {
	return &ImageEntry{
		ID: e.ID, Path: e.Path, Name: e.Name, Size: e.Size,
		LastModified: e.LastModified, CreatedAt: e.CreatedAt,
		Folder: e.Folder, RootPath: e.RootPath,
		Width: e.Width, Height: e.Height, IsVideo: e.IsVideo, IsAudio: isAudioFile(e.Path),
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

// dbSubtreeCount 查询某目录（含子树）在 folders 表中的图片总数。
// ★ 嵌套虚拟根的图片记在外层父根名下：优先用"最长严格前缀"的已注册父根查询，
//   与旧 rootSubtreeCount（folderCount 前缀求和）语义一致。
// ★ folders 表的 root_path 存的是注册时的原始路径（Windows 上为反斜杠），
//   查询必须用注册根的原始形式，不能用正斜杠规范化形式（否则永远查不到 → 计数恒 0）。
// 返回 -1 表示该路径未被任何已注册根目录覆盖（或根本身无 folders 行）。
func (a *App) dbSubtreeCount(folderKey string) int {
	if a.imageDB == nil {
		return -1
	}
	normalized := strings.ReplaceAll(folderKey, "\\", "/")
	// 找到覆盖该路径的已注册根：嵌套根用最长严格前缀父根；普通根要求精确匹配。
	// exactRoot/bestRoot 保存注册时的原始路径形式，与 folders 表写入格式一致。
	a.mu.RLock()
	var bestRoot, bestRootNorm, exactRoot string
	for root := range a.registeredRoots {
		rn := strings.ReplaceAll(root, "\\", "/")
		if rn == normalized {
			exactRoot = root
			continue
		}
		if strings.HasPrefix(normalized, rn+"/") && len(rn) > len(bestRootNorm) {
			bestRoot = root
			bestRootNorm = rn
		}
	}
	a.mu.RUnlock()
	if bestRoot != "" {
		rel := normalized[len(bestRootNorm)+1:]
		if n := a.imageDB.GetFolderSubtreeCount(bestRoot, rel); n >= 0 {
			return n
		}
		return 0 // 已被根覆盖但尚无文件夹行（未扫描）→ 计 0
	}
	// 本身是已注册根：查根行（folder=""）
	if exactRoot == "" {
		return -1
	}
	if n := a.imageDB.GetFolderSubtreeCount(exactRoot, ""); n >= 0 {
		return n
	}
	return 0
}

func (a *App) saveRegisteredRoots() {
	if a.userDataDB == nil {
		return
	}
	a.mu.RLock()
	var importedRoots []database.ImportedRoot
	for r := range a.registeredRoots {
		ft := a.folderTypes[r]
		name := filepath.Base(r)
		ir := database.ImportedRoot{
			Path:       r,
			Name:       name,
			FolderType: ft,
			// ★ AddedAt 留空：MergeRoots 对已存在记录保留原 added_at，
			//   仅对新建记录用当前时间，避免每次保存都把添加日期重置为今天。
			AddedAt: "",
		}
		importedRoots = append(importedRoots, ir)
	}
	a.mu.RUnlock()

	rootList := make([]string, 0, len(importedRoots))
	for _, r := range importedRoots {
		rootList = append(rootList, r.Path)
	}
	fmt.Printf("[删除诊断] saveRegisteredRoots: 正在把 %d 个根写入 SQLite=%v\n", len(importedRoots), rootList)

	// 合并现有元数据（display_name, handle_name 等前端维护的字段）
	if err := a.userDataDB.MergeRoots(importedRoots); err != nil {
		fmt.Printf("[错误] 保存注册目录到 SQLite 失败: %v\n", err)
	}
}

// ==================== 扫描状态标记（防中断丢失/计数半成品） ====================

// 扫描状态持久化在 userDataDir/scan-state.json：
// 扫描开始时同步写入"扫描中"，完成后清除。若程序在扫描中途退出，
// 标记残留 → 下次启动 ensureImageIndex 会补扫该根目录，
// 避免"导入中途退出导致文件夹消失或计数对不上总数"。

const scanStateFileName = "scan-state.json"

type scanStateFile struct {
	Scanning map[string]bool `json:"scanning"`
}

func (a *App) scanStatePath() string {
	return filepath.Join(a.userDataDir, scanStateFileName)
}

func (a *App) loadScanState() map[string]bool {
	data, err := os.ReadFile(a.scanStatePath())
	if err != nil {
		return map[string]bool{}
	}
	var s scanStateFile
	if err := json.Unmarshal(data, &s); err != nil || s.Scanning == nil {
		return map[string]bool{}
	}
	return s.Scanning
}

// setScanInProgress 原子持久化扫描状态。扫描开始前同步调用，完成后清除。
func (a *App) setScanInProgress(rootPath string, inProgress bool) {
	state := a.loadScanState()
	if inProgress {
		state[rootPath] = true
	} else {
		delete(state, rootPath)
	}
	if len(state) == 0 {
		os.Remove(a.scanStatePath())
		return
	}
	if b, err := json.Marshal(scanStateFile{Scanning: state}); err == nil {
		tmp := a.scanStatePath() + ".tmp"
		if os.WriteFile(tmp, b, 0644) == nil {
			os.Rename(tmp, a.scanStatePath())
		}
	}
}

// getInterruptedScans 返回上次中断（标记残留）的扫描根目录
func (a *App) getInterruptedScans() []string {
	state := a.loadScanState()
	var roots []string
	for root, scanning := range state {
		if scanning {
			roots = append(roots, root)
		}
	}
	return roots
}

// ==================== 目录修改时间缓存（刷新跳过未变叶子目录） ====================

const dirMtimesFileName = "dir-mtimes.json"

type dirMtimesFile struct {
	Dirs map[string]int64 `json:"dirs"` // 规范化目录路径 -> 目录 mtime(毫秒)
}

// loadDirMtimes 从磁盘加载目录 mtime 缓存；文件缺失/损坏时返回空 map（安全：不跳过=全扫）。
func loadDirMtimes(path string) map[string]int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]int64{}
	}
	var f dirMtimesFile
	if err := json.Unmarshal(data, &f); err != nil || f.Dirs == nil {
		return map[string]int64{}
	}
	return f.Dirs
}

func (a *App) dirMtimesPath() string {
	return filepath.Join(a.userDataDir, dirMtimesFileName)
}

// saveDirMtimes 原子持久化目录 mtime 缓存（需持 a.mu 调用方自行处理；内部只读拷贝后写盘）。
func (a *App) saveDirMtimes() {
	a.mu.RLock()
	m := make(map[string]int64, len(a.dirMtimes))
	for k, v := range a.dirMtimes {
		m[k] = v
	}
	a.mu.RUnlock()
	b, err := json.Marshal(dirMtimesFile{Dirs: m})
	if err != nil {
		return
	}
	tmp := a.dirMtimesPath() + ".tmp"
	if os.WriteFile(tmp, b, 0644) == nil {
		os.Rename(tmp, a.dirMtimesPath())
	}
}

// recordDirMtime 记录/更新单个目录的 mtime（需持 a.mu 写锁）。
func (a *App) recordDirMtime(dirNorm string, mtimeMillis int64) {
	if a.dirMtimes == nil {
		a.dirMtimes = make(map[string]int64)
	}
	a.dirMtimes[dirNorm] = mtimeMillis
}

// dirMtimeUnchanged 判断目录 mtime 是否与缓存一致（需持 a.mu 读锁）。
func (a *App) dirMtimeUnchanged(dirNorm string, mtimeMillis int64) bool {
	prev, ok := a.dirMtimes[dirNorm]
	return ok && prev == mtimeMillis
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
	// ★ 原子写入：先写临时文件再替换，避免写入中途崩溃/断电/磁盘满导致文件损坏。
	// ★ 覆盖前自动备份上一版到 user-data.json.bak，任何异常覆盖后都能手动恢复。
	tmpPath := a.userDataFile + ".tmp"
	if err := os.WriteFile(tmpPath, bytes, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if _, err := os.Stat(a.userDataFile); err == nil {
		backupPath := a.userDataFile + ".bak"
		if old, err := os.ReadFile(a.userDataFile); err == nil {
			_ = os.WriteFile(backupPath, old, 0644)
		}
	}
	if err := os.Rename(tmpPath, a.userDataFile); err != nil {
		return fmt.Errorf("替换文件失败: %w", err)
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
